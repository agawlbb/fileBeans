#!/usr/bin/env node
// Provision only this app; never revoke certificates, print credentials, or start builds.
import fs from 'node:fs';
import crypto from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

export function validateReleaseConfig(config) {
  if (!/^com\.agawlbb\.myfiles$/.test(config.bundleID) || config.appGroup !== `group.${config.bundleID}` ||
      config.repository !== 'agawlbb/fileBeans' || config.environment !== 'testflight' ||
      !/^[A-Z0-9]{10}$/.test(config.teamID)) throw new Error('Unexpected release configuration');
  return config;
}

export function validateProfile(summary, config, bundleID, certificateSHA256) {
  const expires = Date.parse(summary.expires);
  if (summary.appID !== `${config.teamID}.${bundleID}` ||
      !summary.teams.includes(config.teamID) || !summary.groups.includes(config.appGroup) ||
      summary.debug || summary.deviceRestricted || summary.enterprise ||
      !summary.certificates.includes(certificateSHA256) ||
      !summary.keychainGroups.some(value => value === `${config.teamID}.*` || value === `${config.teamID}.${config.bundleID}`) ||
      (bundleID === config.bundleID && !summary.keychainGroups.some(value =>
        value === `${config.teamID}.*` || value === `${config.teamID}.${config.bundleID}.vaults`)) ||
      !Number.isFinite(expires) || expires < Date.now() + 86400000) {
    throw new Error('Profile must match the app, App Group, shared Keychain, distribution certificate, and App Store distribution type');
  }
}

export function selectBundle(document, identifier) {
  // Apple's identifier filter also returns prefix matches, including extensions.
  const exact = document.data.filter(value => value.attributes.identifier === identifier);
  if (exact.length > 1 || document.links?.next) throw new Error('Ambiguous bundle identifier inventory');
  return exact[0];
}

function command(executable, args, input) {
  const result = spawnSync(executable, args, { input, encoding: 'utf8', maxBuffer: 8 * 1024 * 1024 });
  // Do not include argv, stdin, stdout, or stderr in exceptions: they may hold secrets.
  if (result.error || result.status !== 0) throw new Error(`${executable} operation failed; no credential output emitted`);
  return result.stdout;
}

export function summarizeProfile(data) {
  const plist = command('openssl', ['cms', '-verify', '-inform', 'DER', '-noverify'], data);
  return JSON.parse(command('python3', ['-c', `
import sys,plistlib,json,hashlib
p=plistlib.loads(sys.stdin.buffer.read()); e=p['Entitlements']
print(json.dumps({'appID':e.get('application-identifier'), 'teams':p.get('TeamIdentifier',[]),
'groups':e.get('com.apple.security.application-groups',[]), 'keychainGroups':e.get('keychain-access-groups',[]),
'debug':e.get('get-task-allow',False), 'deviceRestricted':bool(p.get('ProvisionedDevices')),
'enterprise':p.get('ProvisionsAllDevices',False), 'expires':p['ExpirationDate'].isoformat()+'Z',
'certificates':[hashlib.sha256(c).hexdigest() for c in p.get('DeveloperCertificates',[])]}))
`], plist));
}

async function main() {
  const config = validateReleaseConfig(JSON.parse(fs.readFileSync(new URL('../Config/Release.json', import.meta.url))));
  const fromEnvironment = process.env.ASC_PRIVATE_KEY_BASE64 !== undefined;
  const auth = fromEnvironment ? { key_id: process.env.ASC_KEY_ID, issuer_id: process.env.ASC_ISSUER_ID } :
    JSON.parse(fs.readFileSync(process.env.ASC_CONFIG || '/run/apple-credentials/api.json'));
  if (!auth.key_id || !auth.issuer_id || (fromEnvironment && !process.env.ASC_PRIVATE_KEY_BASE64)) {
    throw new Error('Missing App Store Connect API credential');
  }
  const keyBytes = fromEnvironment ? Buffer.from(process.env.ASC_PRIVATE_KEY_BASE64, 'base64') : fs.readFileSync(auth.private_key_path);
  const key = crypto.createPrivateKey(keyBytes);
  const encode = value => Buffer.from(JSON.stringify(value)).toString('base64url');
  const now = Math.floor(Date.now() / 1000);
  const unsigned = `${encode({ alg: 'ES256', kid: auth.key_id, typ: 'JWT' })}.${encode({ iss: auth.issuer_id, iat: now - 10, exp: now + 600, aud: 'appstoreconnect-v1' })}`;
  const token = unsigned + '.' + crypto.sign('sha256', Buffer.from(unsigned), { key, dsaEncoding: 'ieee-p1363' }).toString('base64url');
  async function request(path, method = 'GET', data) {
    const response = await fetch('https://api.appstoreconnect.apple.com' + path, {
      method, redirect: 'error', signal: AbortSignal.timeout(30000),
      headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' },
      ...(data ? { body: JSON.stringify({ data }) } : {})
    });
    if (!response.ok) {
      const document = await response.json().catch(() => ({}));
      const codes = (document.errors || []).map(error => String(error.code || '').replace(/[^A-Za-z0-9_.-]/g, '')).filter(Boolean);
      throw new Error(`Apple setup request failed: ${method} ${path.split('?')[0]} HTTP ${response.status}${codes.length ? ' (' + codes.join(', ') + ')' : ''}`);
    }
    return response.json();
  }
  const query = fields => new URLSearchParams(fields).toString();
  const gh = args => command('gh', args);
  function secret(name, value) {
    command('gh', ['secret', 'set', name, '--repo', config.repository, '--env', config.environment], value);
    console.log(`Configured GitHub environment secret: ${name}`);
  }
  async function bundle(identifier) {
    const result = await request('/v1/bundleIds?' + query({ 'filter[identifier]': identifier, limit: '200' }));
    return selectBundle(result, identifier);
  }
  const operation = process.argv[2] || 'inspect';
  if (operation === 'configure-github') {
    secret('ASC_KEY_ID', auth.key_id);
    secret('ASC_ISSUER_ID', auth.issuer_id);
    secret('ASC_PRIVATE_KEY_BASE64', keyBytes.toString('base64'));
    for (const [name, value] of [['APPLE_TEAM_ID', config.teamID], ['BUNDLE_ID_PREFIX', config.bundleID]]) {
      gh(['variable', 'set', name, '--repo', config.repository, '--env', config.environment, '--body', value]);
      console.log(`Configured GitHub environment variable: ${name}`);
    }
    return;
  }
  if (!['inspect', 'register', 'profiles', 'preflight'].includes(operation)) throw new Error('Use inspect, register, configure-github, profiles, or preflight');
  const identifiers = [config.bundleID, `${config.bundleID}.FileProvider`, `${config.bundleID}.Share`];
  const registrations = [];
  for (const identifier of identifiers) {
    let current = await bundle(identifier);
    if (!current && operation === 'register') {
      current = (await request('/v1/bundleIds', 'POST', { type: 'bundleIds', attributes: {
        identifier, name: identifier === config.bundleID ? config.name : `${config.name} Files`, platform: 'IOS'
      } })).data;
    }
    if (!current) {
      if (operation === 'profiles') throw new Error('Register all bundle IDs before creating profiles');
      registrations.push({ identifier, registered: false }); continue;
    }
    let capabilities = (await request(`/v1/bundleIds/${current.id}/bundleIdCapabilities`)).data;
    if (operation === 'register' && !capabilities.some(c => c.attributes.capabilityType === 'APP_GROUPS')) {
      await request('/v1/bundleIdCapabilities', 'POST', { type: 'bundleIdCapabilities',
        attributes: { capabilityType: 'APP_GROUPS' }, relationships: { bundleId: { data: { type: 'bundleIds', id: current.id } } } });
      capabilities = (await request(`/v1/bundleIds/${current.id}/bundleIdCapabilities`)).data;
    }
    registrations.push({ identifier, registered: true, id: current.id,
      appGroupsEnabled: capabilities.some(c => c.attributes.capabilityType === 'APP_GROUPS') });
  }
  if (operation === 'preflight') {
    const blockers = [];
    for (const name of ['APPLE_TEAM_ID', 'BUNDLE_ID_PREFIX', 'DISTRIBUTION_P12_BASE64', 'DISTRIBUTION_P12_PASSWORD',
                        'APP_PROFILE_BASE64', 'PROVIDER_PROFILE_BASE64', 'SHARE_PROFILE_BASE64']) {
      if (!process.env[name]) blockers.push(`Missing environment setting: ${name}`);
    }
    if (process.env.APPLE_TEAM_ID && process.env.APPLE_TEAM_ID !== config.teamID) blockers.push('Apple team does not match release configuration');
    if (process.env.BUNDLE_ID_PREFIX && process.env.BUNDLE_ID_PREFIX !== config.bundleID) blockers.push('Bundle ID does not match release configuration');
    if (registrations.some(r => !r.registered || !r.appGroupsEnabled)) blockers.push('Register all bundle IDs and their App Groups capability');
    const apps = await request('/v1/apps?' + query({ 'filter[bundleId]': config.bundleID, 'fields[apps]': 'name,bundleId,sku' }));
    apps.data = apps.data.filter(app => app.attributes.bundleId === config.bundleID);
    if (apps.data.length !== 1) blockers.push(`Create the ${config.name} iOS app record on the App Store Connect website`);
    const certificate = (await request(`/v1/certificates/${config.certificateID}`)).data;
    const cert = new crypto.X509Certificate(Buffer.from(certificate.attributes.certificateContent, 'base64'));
    if (certificate.attributes.certificateType !== 'DISTRIBUTION' || cert.validToDate.getTime() < Date.now() + 86400000) {
      blockers.push('Retained distribution certificate is expired or invalid');
    }
    const sha = crypto.createHash('sha256').update(cert.raw).digest('hex');
    if (process.env.DISTRIBUTION_P12_BASE64 && process.env.DISTRIBUTION_P12_PASSWORD) {
      const pem = command('openssl', ['pkcs12', '-clcerts', '-nokeys', '-passin', 'env:DISTRIBUTION_P12_PASSWORD'],
                          Buffer.from(process.env.DISTRIBUTION_P12_BASE64, 'base64'));
      const packaged = new crypto.X509Certificate(pem);
      if (!packaged.raw.equals(cert.raw)) blockers.push('P12 does not contain the retained Apple distribution certificate');
      const privatePEM = command('openssl', ['pkcs12', '-nocerts', '-noenc', '-passin', 'env:DISTRIBUTION_P12_PASSWORD'],
                                 Buffer.from(process.env.DISTRIBUTION_P12_BASE64, 'base64'));
      const publicKey = crypto.createPublicKey(crypto.createPrivateKey(privatePEM)).export({ type: 'spki', format: 'der' });
      if (!publicKey.equals(cert.publicKey.export({ type: 'spki', format: 'der' }))) blockers.push('P12 private key does not match the distribution certificate');
    }
    for (const [name, suffix] of [['APP_PROFILE_BASE64', ''], ['PROVIDER_PROFILE_BASE64', '.FileProvider'], ['SHARE_PROFILE_BASE64', '.Share']]) {
      if (process.env[name]) {
        try { validateProfile(summarizeProfile(Buffer.from(process.env[name], 'base64')), config, config.bundleID + suffix, sha); }
        catch { blockers.push(`${name} does not match the app, App Group, shared Keychain, certificate, or expiry requirements`); }
      }
    }
    if (process.env.RELEASE_OPERATION && process.env.RELEASE_OPERATION !== 'preflight') {
      const number = process.env.BUILD_NUMBER || '';
      if (!/^[1-9][0-9]{0,8}$/.test(number)) blockers.push('Choose a positive build number of at most nine digits');
      else if (apps.data.length === 1) {
        const existing = await request('/v1/builds?' + query({ 'filter[app]': apps.data[0].id, 'filter[version]': number, limit: '1' }));
        if (existing.data.length) blockers.push('That build number already exists in App Store Connect');
      }
    }
    if (blockers.length) throw new Error(blockers.join('\n'));
    console.log('Signing preflight passed: registered app, all profiles, App Group, Keychain sharing, and retained distribution certificate verified.');
    return;
  }
  if (operation === 'profiles') {
    const certificate = (await request(`/v1/certificates/${config.certificateID}`)).data;
    if (certificate.attributes.certificateType !== 'DISTRIBUTION' || Date.parse(certificate.attributes.expirationDate) < Date.now() + 86400000) {
      throw new Error('Selected existing distribution certificate is not valid');
    }
    const sha = crypto.createHash('sha256').update(Buffer.from(certificate.attributes.certificateContent, 'base64')).digest('hex');
    for (const registered of registrations) {
      const app = registered.identifier === config.bundleID;
      const share = registered.identifier.endsWith('.Share');
      const name = app ? 'myFiles App Store' : share ? 'myFiles Share App Store' : 'myFiles Files App Store';
      const inventory = await request('/v1/profiles?' + query({ 'filter[name]': name, limit: '200' }));
      if (inventory.links?.next) throw new Error('Unexpectedly paginated provisioning profile inventory');
      let profile = inventory.data.find(p => p.attributes.name === name && p.attributes.profileState === 'ACTIVE' && Date.parse(p.attributes.expirationDate) > Date.now() + 86400000);
      if (!profile) {
        profile = (await request('/v1/profiles', 'POST', { type: 'profiles',
          attributes: { name, profileType: 'IOS_APP_STORE' }, relationships: {
            bundleId: { data: { type: 'bundleIds', id: registered.id } },
            certificates: { data: [{ type: 'certificates', id: config.certificateID }] }
          } })).data;
      }
      const summary = summarizeProfile(Buffer.from(profile.attributes.profileContent, 'base64'));
      validateProfile(summary, config, registered.identifier, sha);
      secret(app ? 'APP_PROFILE_BASE64' : share ? 'SHARE_PROFILE_BASE64' : 'PROVIDER_PROFILE_BASE64', profile.attributes.profileContent);
      console.log(`Validated App Store profile for ${registered.identifier}; expires ${summary.expires}`);
    }
    return;
  }
  const apps = await request('/v1/apps?' + query({ 'filter[bundleId]': config.bundleID, 'fields[apps]': 'name,bundleId,sku' }));
  apps.data = apps.data.filter(app => app.attributes.bundleId === config.bundleID);
  console.log(JSON.stringify({ registrations, app: apps.data[0] ? {
    id: apps.data[0].id, ...apps.data[0].attributes
  } : null, websiteRegistration: { appGroup: config.appGroup, name: config.name, sku: config.sku, primaryLocale: config.primaryLocale } }, null, 2));
}

if (process.argv[1] && fileURLToPath(import.meta.url) === fs.realpathSync(process.argv[1])) {
  main().catch(error => { console.error(error.message); process.exitCode = 1; });
}
