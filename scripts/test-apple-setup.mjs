import assert from 'node:assert/strict';
import fs from 'node:fs';
import test from 'node:test';
import { selectBundle, validateProfile, validateReleaseConfig } from './apple-setup.mjs';

const config = JSON.parse(fs.readFileSync(new URL('../Config/Release.json', import.meta.url)));
const profile = () => ({ appID: `${config.teamID}.${config.bundleID}`, teams: [config.teamID],
  groups: [config.appGroup], keychainGroups: [`${config.teamID}.*`], certificates: ['fixture-sha256'],
  debug: false, deviceRestricted: false, enterprise: false, expires: new Date(Date.now() + 30 * 86400000).toISOString() });

test('profile binds the correct app, group, team, Keychain and certificate', () => {
  assert.doesNotThrow(() => validateProfile(profile(), config, config.bundleID, 'fixture-sha256'));
  for (const change of [{ appID: `${config.teamID}.another.app` }, { teams: ['OTHERTEAM1'] }, { groups: [] },
                        { keychainGroups: [`${config.teamID}.unrelated`] }, { certificates: ['wrong-certificate'] }]) {
    assert.throws(() => validateProfile({ ...profile(), ...change }, config, config.bundleID, 'fixture-sha256'));
  }
});
test('development, ad hoc, enterprise, expired and malformed profiles are rejected', () => {
  for (const change of [{ debug: true }, { deviceRestricted: true }, { enterprise: true },
                        { expires: '2000-01-01T00:00:00Z' }, { expires: 'invalid' }]) {
    assert.throws(() => validateProfile({ ...profile(), ...change }, config, config.bundleID, 'fixture-sha256'));
  }
});
test('extension profile must allow the shared main-app Keychain group', () => {
  const extension = { ...profile(), appID: `${config.teamID}.${config.bundleID}.FileProvider`,
                      keychainGroups: [`${config.teamID}.${config.bundleID}`] };
  assert.doesNotThrow(() => validateProfile(extension, config, `${config.bundleID}.FileProvider`, 'fixture-sha256'));
  extension.keychainGroups = [`${config.teamID}.${config.bundleID}.FileProvider`];
  assert.throws(() => validateProfile(extension, config, `${config.bundleID}.FileProvider`, 'fixture-sha256'));
});
test('main app profile must also allow the app-only vault Keychain group', () => {
  const value = { ...profile(), keychainGroups: [`${config.teamID}.${config.bundleID}`] };
  assert.throws(() => validateProfile(value, config, config.bundleID, 'fixture-sha256'));
  value.keychainGroups.push(`${config.teamID}.${config.bundleID}.vaults`);
  assert.doesNotThrow(() => validateProfile(value, config, config.bundleID, 'fixture-sha256'));
});
test('release setup cannot write to another repository or App Group', () => {
  assert.doesNotThrow(() => validateReleaseConfig(config));
  for (const change of [{ repository: 'other/repository' }, { appGroup: 'group.unrelated' }, { environment: 'production' }]) {
    assert.throws(() => validateReleaseConfig({ ...config, ...change }));
  }
});
test('Apple prefix filters cannot confuse the app with its extension', () => {
  const app = { id: 'app', attributes: { identifier: config.bundleID } };
  const extension = { id: 'extension', attributes: { identifier: `${config.bundleID}.FileProvider` } };
  assert.equal(selectBundle({ data: [extension, app] }, config.bundleID), app);
  assert.equal(selectBundle({ data: [extension, app] }, extension.attributes.identifier), extension);
  assert.equal(selectBundle({ data: [extension] }, config.bundleID), undefined);
});

test('share extension profile uses the shared Keychain without the vault key group', () => {
  const share = { ...profile(), appID: `${config.teamID}.${config.bundleID}.Share`,
    keychainGroups: [`${config.teamID}.${config.bundleID}`] };
  assert.doesNotThrow(() => validateProfile(share, config, `${config.bundleID}.Share`, 'fixture-sha256'));
  share.keychainGroups = [`${config.teamID}.${config.bundleID}.Share`];
  assert.throws(() => validateProfile(share, config, `${config.bundleID}.Share`, 'fixture-sha256'));
});
