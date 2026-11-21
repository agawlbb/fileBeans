package service

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/sys/unix"
)

var ErrBrokerCapabilities = errors.New("vault broker capabilities unavailable")

// Local gocryptfs mounts preserve the caller's ownership by switching thread
// credentials. UID 0 and a bounding set alone do not grant that operation.
var brokerCapabilities = []struct {
	bit  uint
	name string
}{
	{unix.CAP_CHOWN, "CAP_CHOWN"},
	{unix.CAP_DAC_OVERRIDE, "CAP_DAC_OVERRIDE"},
	{unix.CAP_FOWNER, "CAP_FOWNER"},
	{unix.CAP_SETGID, "CAP_SETGID"},
	{unix.CAP_SETUID, "CAP_SETUID"},
	{unix.CAP_SYS_ADMIN, "CAP_SYS_ADMIN"},
}

func checkBrokerCapabilities(data [2]unix.CapUserData) error {
	var missing []string
	for _, capability := range brokerCapabilities {
		word, bit := capability.bit/32, uint32(1)<<(capability.bit%32)
		if data[word].Permitted&bit == 0 || data[word].Effective&bit == 0 {
			missing = append(missing, capability.name)
		}
	}
	if len(missing) != 0 {
		return fmt.Errorf("%w: require effective and permitted %s; check service capability configuration and restart the broker", ErrBrokerCapabilities, strings.Join(missing, ", "))
	}
	return nil
}

func requireBrokerCapabilities() error {
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	var data [2]unix.CapUserData
	if err := unix.Capget(&header, &data[0]); err != nil {
		return fmt.Errorf("%w: cannot inspect Linux capabilities", ErrBrokerCapabilities)
	}
	return checkBrokerCapabilities(data)
}
