//
// Use and distribution licensed under the Apache license version 2.
//
// See the COPYING file in the root project directory for full text.
//

package snapshot

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
)

const (
	sysClassNet = "/sys/class/net"
)

func NetIfacesCloneContent() []string {
	var fileSpecs []string
	ifaceEntries := []string{
		"addr_assign_type",
		// intentionally avoid to clone "address" to avoid to leak any host-idenfifiable data.
	}

	// some files are created only if the network interface is of given type (e.g. SRIOV).
	// so we need to list only what's there
	ifaceOptionalEntries := []string{
		// we know we are on linux, so we hardcode the path separator
		"device/physfn",
		"device/sriov_*",
		"device/virtfn*",
	}
	entries, err := ioutil.ReadDir(sysClassNet)
	if err != nil {
		// we should not import context, hence we can't Warn()
		return fileSpecs
	}
	for _, entry := range entries {
		netName := entry.Name()
		netPath := filepath.Join(sysClassNet, netName)
		dest, err := os.Readlink(netPath)
		if err != nil {
			continue
		}
		if strings.Contains(dest, "devices/virtual/net") {
			// there is no point in cloning data for virtual devices,
			// because ghw concerns itself with HardWare.
			continue
		}

		// so, first copy the symlink itself
		fileSpecs = append(fileSpecs, netPath)

		// now we have to clone the content of the actual network interface
		// data related (and found into a subdir of) the backing hardware
		// device
		netIface := filepath.Clean(filepath.Join(sysClassNet, dest))
		for _, ifaceEntry := range ifaceEntries {
			fileSpecs = append(fileSpecs, filepath.Join(netIface, ifaceEntry))
		}

		for _, ifaceOptionalEntry := range ifaceOptionalEntries {
			netIfaceOptEntry := filepath.Join(netIface, ifaceOptionalEntry)
			if matches, err := filepath.Glob(netIfaceOptEntry); len(matches) > 0 && err == nil {
				fileSpecs = append(fileSpecs, netIfaceOptEntry)
			}
		}
	}

	return fileSpecs
}
