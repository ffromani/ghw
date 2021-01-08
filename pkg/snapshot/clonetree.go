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
	"strconv"
	"strings"

	pciaddr "github.com/jaypipes/ghw/pkg/pci/address"
)

func CloneTreeInto(scratchDir string) error {
	var err error

	var createPaths = []string{
		"proc",
		"etc",
		"sys/block",
		"sys/bus/pci/devices",
		"sys/devices",
		"sys/devices/system/cpu",
		"sys/devices/system/memory",
		"sys/devices/system/node",
	}

	for _, path := range createPaths {
		if err = os.MkdirAll(filepath.Join(scratchDir, path), os.ModePerm); err != nil {
			return err
		}
	}

	if err = createPseudofiles(scratchDir); err != nil {
		return err
	}
	if err = createBlockDevices(scratchDir); err != nil {
		return err
	}
	if err = createSysDevicesSystemCPU(scratchDir); err != nil {
		return err
	}
	if err = createSysDevicesSystemMemory(scratchDir); err != nil {
		return err
	}
	if err = createSysDevicesSystemNode(scratchDir); err != nil {
		return err
	}
	if err = createSysDevicesPCI(scratchDir); err != nil {
		return err
	}
	if err = createSysBusPCIDevices(scratchDir); err != nil {
		return err
	}
	return nil
}

// Attempting to tar up pseudofiles like /proc/cpuinfo is an exercise in
// futility. Notably, the pseudofiles, when read by syscalls, do not return the
// number of bytes read. This causes the tar writer to write zero-length files.
//
// Instead, it is necessary to build a directory structure in a tmpdir and
// create actual files with copies of the pseudofile contents
func createPseudofiles(buildDir string) error {
	createPseudofilePaths := []string{
		"/proc/cpuinfo",
		"/proc/meminfo",
		"/etc/mtab",
	}

	for _, path := range createPseudofilePaths {
		err := copyPseudoFile(path, filepath.Join(buildDir, path))
		if err != nil {
			return err
		}
	}
	return nil
}

func copyPseudoFile(path, targetPath string) error {
	buf, err := ioutil.ReadFile(path)
	if err != nil {
		return err
	}
	trace("creating %q\n", targetPath)
	f, err := os.Create(targetPath)
	if err != nil {
		return err
	}
	if _, err = f.Write(buf); err != nil {
		return err
	}
	f.Close()
	return nil
}

func createBlockDevices(buildDir string) error {
	// Grab all the block device pseudo-directories from /sys/block symlinks
	// (excluding loopback devices) and inject them into our build filesystem
	// with all but the circular symlink'd subsystem directories
	devLinks, err := ioutil.ReadDir("/sys/block")
	if err != nil {
		return err
	}
	for _, devLink := range devLinks {
		dname := devLink.Name()
		if strings.HasPrefix(dname, "loop") {
			continue
		}
		devPath := filepath.Join("/sys/block", dname)
		trace("processing block device %q\n", devPath)

		// from the sysfs layout, we know this is always a symlink
		linkContentPath, err := os.Readlink(devPath)
		if err != nil {
			return err
		}
		trace("link target for block device %q is %q\n", devPath, linkContentPath)

		// Create a symlink in our build filesystem that is a directory
		// pointing to the actual device bus path where the block device's
		// information directory resides
		linkPath := filepath.Join(buildDir, "sys/block", dname)
		linkTargetPath := filepath.Join(
			buildDir,
			"sys/block",
			strings.TrimPrefix(linkContentPath, string(os.PathSeparator)),
		)
		trace("creating device directory %q\n", linkTargetPath)
		if err = os.MkdirAll(linkTargetPath, os.ModePerm); err != nil {
			return err
		}

		trace("linking device directory %s to %s\n", linkPath, linkContentPath)
		// Make sure the link target is a relative path!
		// if we use absolute path, the link target will be an absolute path starting
		// with buildDir, hence the snapshot will contain broken link.
		// Otherwise, the unpack directory will never have the same prefix of buildDir!
		if err = os.Symlink(linkContentPath, linkPath); err != nil {
			return err
		}
		// Now read the source block device directory and populate the
		// newly-created target link in the build directory with the
		// appropriate block device pseudofiles
		srcDeviceDir := filepath.Join(
			"/sys/block",
			strings.TrimPrefix(linkContentPath, string(os.PathSeparator)),
		)
		trace("creating device directory %q from %q\n", linkTargetPath, srcDeviceDir)
		if err = createBlockDeviceDir(linkTargetPath, srcDeviceDir); err != nil {
			return err
		}
	}
	return nil
}

func createBlockDeviceDir(buildDeviceDir string, srcDeviceDir string) error {
	// Populate the supplied directory (in our build filesystem) with all the
	// appropriate information pseudofile contents for the block device.
	devName := filepath.Base(srcDeviceDir)
	devFiles, err := ioutil.ReadDir(srcDeviceDir)
	if err != nil {
		return err
	}
	for _, f := range devFiles {
		fname := f.Name()
		fp := filepath.Join(srcDeviceDir, fname)
		fi, err := os.Lstat(fp)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			// Ignore any symlinks in the deviceDir since they simply point to
			// either self-referential links or information we aren't
			// interested in like "subsystem"
			continue
		} else if fi.IsDir() {
			if strings.HasPrefix(fname, devName) {
				// We're interested in are the directories that begin with the
				// block device name. These are directories with information
				// about the partitions on the device
				buildPartitionDir := filepath.Join(
					buildDeviceDir, fname,
				)
				srcPartitionDir := filepath.Join(
					srcDeviceDir, fname,
				)
				trace("creating partition directory %q\n", buildPartitionDir)
				err = os.MkdirAll(buildPartitionDir, os.ModePerm)
				if err != nil {
					return err
				}
				err = createPartitionDir(buildPartitionDir, srcPartitionDir)
				if err != nil {
					return err
				}
			}
		} else if fi.Mode().IsRegular() {
			// Regular files in the block device directory are both regular and
			// pseudofiles containing information such as the size (in sectors)
			// and whether the device is read-only
			buf, err := ioutil.ReadFile(fp)
			if err != nil {
				return err
			}
			targetPath := filepath.Join(buildDeviceDir, fname)
			trace("creating %q\n", targetPath)
			f, err := os.Create(targetPath)
			if err != nil {
				return err
			}
			if _, err = f.Write(buf); err != nil {
				return err
			}
			f.Close()
		}
	}
	// There is a special file $DEVICE_DIR/queue/rotational that, for some hard
	// drives, contains a 1 or 0 indicating whether the device is a spinning
	// disk or not
	srcQueueDir := filepath.Join(
		srcDeviceDir,
		"queue",
	)
	buildQueueDir := filepath.Join(
		buildDeviceDir,
		"queue",
	)
	err = os.MkdirAll(buildQueueDir, os.ModePerm)
	if err != nil {
		return err
	}
	fp := filepath.Join(srcQueueDir, "rotational")
	buf, err := ioutil.ReadFile(fp)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(buildQueueDir, "rotational")
	trace("creating %q\n", targetPath)
	f, err := os.Create(targetPath)
	if err != nil {
		return err
	}
	if _, err = f.Write(buf); err != nil {
		return err
	}
	f.Close()

	return nil
}

func createPartitionDir(buildPartitionDir string, srcPartitionDir string) error {
	// Populate the supplied directory (in our build filesystem) with all the
	// appropriate information pseudofile contents for the partition.
	partFiles, err := ioutil.ReadDir(srcPartitionDir)
	if err != nil {
		return err
	}
	for _, f := range partFiles {
		fname := f.Name()
		fp := filepath.Join(srcPartitionDir, fname)
		fi, err := os.Lstat(fp)
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			// Ignore any symlinks in the partition directory since they simply
			// point to information we aren't interested in like "subsystem"
			continue
		} else if fi.IsDir() {
			// The subdirectories in the partition directory are not
			// interesting for us. They have information about power events and
			// traces
			continue
		} else if fi.Mode().IsRegular() {
			// Regular files in the block device directory are both regular and
			// pseudofiles containing information such as the size (in sectors)
			// and whether the device is read-only
			buf, err := ioutil.ReadFile(fp)
			if err != nil {
				return err
			}
			targetPath := filepath.Join(buildPartitionDir, fname)
			trace("creating %q\n", targetPath)
			f, err := os.Create(targetPath)
			if err != nil {
				return err
			}
			if _, err = f.Write(buf); err != nil {
				return err
			}
			f.Close()
		}
	}
	return nil
}

func isCPUEntry(cname string) bool {
	if !strings.HasPrefix(cname, "cpu") {
		return false
	}

	if _, err := strconv.Atoi(cname[3:]); err != nil {
		// doesn't look like cpu0, cpu42... better skip it.
		return false
	}
	return true
}

func createSysDevicesSystemCPU(buildDir string) error {
	devSysCPU := "/sys/devices/system/cpu"
	cpuEntries, err := ioutil.ReadDir(devSysCPU)
	if err != nil {
		return err
	}
	for _, cpuEntry := range cpuEntries {
		cname := cpuEntry.Name()
		if !isCPUEntry(cname) {
			continue
		}

		trace("creating %q\n", cname)
		cpuTopoDir := filepath.Join(devSysCPU, cpuEntry.Name(), "topology")
		if err = os.MkdirAll(filepath.Join(buildDir, cpuTopoDir), os.ModePerm); err != nil {
			return err
		}

		cpuTopoEntries, err := ioutil.ReadDir(cpuTopoDir)
		if err != nil {
			return err
		}

		for _, cpuTopoEntry := range cpuTopoEntries {
			path := filepath.Join(cpuTopoDir, cpuTopoEntry.Name())
			if err := copyPseudoFile(path, filepath.Join(buildDir, path)); err != nil {
				return err
			}
		}

		cpuCacheDir := filepath.Join(devSysCPU, cpuEntry.Name(), "cache")
		if err = os.MkdirAll(filepath.Join(buildDir, cpuCacheDir), os.ModePerm); err != nil {
			return err
		}

		cpuCacheEntries, err := ioutil.ReadDir(cpuCacheDir)
		if err != nil {
			return err
		}

		for _, cpuCacheEntry := range cpuCacheEntries {
			ccname := cpuCacheEntry.Name()

			if !strings.HasPrefix(ccname, "index") {
				continue
			}

			if _, err := strconv.Atoi(ccname[5:]); err != nil {
				// doesn't look like index0, index42... better skip it.
				continue
			}

			trace("creating %q\n", ccname)
			cpuCacheEntryDir := filepath.Join(cpuCacheDir, ccname)

			if err := os.MkdirAll(filepath.Join(buildDir, cpuCacheEntryDir), os.ModePerm); err != nil {
				return err
			}

			cpuCacheIndexEntries, err := ioutil.ReadDir(cpuCacheEntryDir)
			if err != nil {
				return err
			}

			for _, cpuCacheIndexEntry := range cpuCacheIndexEntries {
				path := filepath.Join(cpuCacheEntryDir, cpuCacheIndexEntry.Name())
				if err := copyPseudoFile(path, filepath.Join(buildDir, path)); err != nil {
					return err
				}
			}
		}

	}
	return nil
}

func createSysDevicesSystemMemory(buildDir string) error {
	devSysMemory := "/sys/devices/system/memory"

	for _, pseudoFile := range []string{"block_size_bytes"} {
		path := filepath.Join(devSysMemory, pseudoFile)
		if err := copyPseudoFile(path, filepath.Join(buildDir, path)); err != nil {
			return err
		}
	}

	memoryEntries, err := ioutil.ReadDir(devSysMemory)
	if err != nil {
		return err
	}

	for _, memoryEntry := range memoryEntries {
		mname := memoryEntry.Name()
		if !strings.HasPrefix(mname, "memory") {
			continue
		}

		if _, err := strconv.Atoi(mname[6:]); err != nil {
			// doesn't look like memory0, memory42... better skip it.
			continue
		}

		trace("creating %q\n", mname)
		if err := os.MkdirAll(filepath.Join(buildDir, devSysMemory, mname), os.ModePerm); err != nil {
			return err
		}

		for _, pseudoFile := range []string{"online", "state"} {
			path := filepath.Join(devSysMemory, mname, pseudoFile)
			if err := copyPseudoFile(path, filepath.Join(buildDir, path)); err != nil {
				return err
			}
		}
	}
	return nil
}

func createSysDevicesSystemNode(buildDir string) error {
	devSysNode := "/sys/devices/system/node"

	for _, pseudoFile := range []string{"has_cpu", "has_memory", "has_normal_memory", "online", "possible"} {
		path := filepath.Join(devSysNode, pseudoFile)
		if err := copyPseudoFile(path, filepath.Join(buildDir, path)); err != nil {
			return err
		}
	}

	nodeEntries, err := ioutil.ReadDir(devSysNode)
	if err != nil {
		return err
	}

	for _, nodeEntry := range nodeEntries {
		nname := nodeEntry.Name()
		if !strings.HasPrefix(nname, "node") {
			continue
		}

		if _, err := strconv.Atoi(nname[4:]); err != nil {
			// doesn't look like node0, node42... better skip it.
			continue
		}

		trace("creating %q\n", nname)
		if err := os.MkdirAll(filepath.Join(buildDir, devSysNode, nname), os.ModePerm); err != nil {
			return err
		}

		devSysNodeNodeX := filepath.Join(devSysNode, nname)
		perNodeEntries, err := ioutil.ReadDir(devSysNodeNodeX)
		if err != nil {
			return err
		}

		for _, perNodeEntry := range perNodeEntries {
			for _, pseudoFile := range []string{"cpulist", "distance"} {
				path := filepath.Join(devSysNodeNodeX, pseudoFile)
				if err := copyPseudoFile(path, filepath.Join(buildDir, path)); err != nil {
					return err
				}
			}

			pnname := perNodeEntry.Name()
			if !isCPUEntry(pnname) {
				continue
			}

			trace("creating %q\n", pnname)
			// from sysfs layout, we know already we know these are symlinks
			target, err := os.Readlink(filepath.Join(devSysNodeNodeX, pnname))
			if err != nil {
				return err
			}

			if err := os.Symlink(target, filepath.Join(buildDir, devSysNodeNodeX, pnname)); err != nil {
				return err
			}
		}
	}
	return nil
}

func isPCIAddress(s string) bool {
	return pciaddr.FromString(s) != nil
}

func createSysDevicesPCI(buildDir string) error {
	sysDevPath := "/sys/devices"

	sysDevEntries, err := ioutil.ReadDir(sysDevPath)
	if err != nil {
		return err
	}

	for _, sysDevEntry := range sysDevEntries {
		dname := sysDevEntry.Name()
		if !strings.HasPrefix(dname, "pci") {
			continue
		}

		sysDevPCIBus := filepath.Join(sysDevPath, dname)
		trace("creating %q\n", sysDevPCIBus)

		if err := os.MkdirAll(filepath.Join(buildDir, sysDevPCIBus), os.ModePerm); err != nil {
			return err
		}

		perBusEntries, err := ioutil.ReadDir(sysDevPCIBus)
		if err != nil {
			return err
		}

		for _, perBusEntry := range perBusEntries {
			pbname := perBusEntry.Name()
			if !isPCIAddress(pbname) {
				continue
			}

			trace("creating %q\n", pbname)
			if err := os.MkdirAll(filepath.Join(buildDir, sysDevPCIBus, pbname), os.ModePerm); err != nil {
				return err
			}

			if err := copyPseudoFiles(buildDir, filepath.Join(sysDevPCIBus, pbname), []string{"local_cpulist", "modalias"}); err != nil {
				return err
			}
		}

		sysDevPCIBusMeta := filepath.Join(sysDevPCIBus, "pci_bus")
		metaEntries, err := ioutil.ReadDir(sysDevPCIBusMeta)
		if err != nil {
			return err
		}

		for _, metaEntry := range metaEntries {
			mname := metaEntry.Name()

			sysDevPCIBusMetaBus := filepath.Join(sysDevPCIBusMeta, mname)
			trace("creating %q\n", sysDevPCIBusMetaBus)

			if err := os.MkdirAll(filepath.Join(buildDir, sysDevPCIBusMetaBus), os.ModePerm); err != nil {
				return err
			}

			if err := copyPseudoFiles(buildDir, sysDevPCIBusMetaBus, []string{"cpulistaffinity"}); err != nil {
				return err
			}
		}
	}

	return nil
}

func createSysBusPCIDevices(buildDir string) error {
	sysBusPciDevPath := "/sys/bus/pci/devices"

	sysBusPciDevEntries, err := ioutil.ReadDir(sysBusPciDevPath)
	if err != nil {
		return err
	}

	for _, sysBusPciDevEntry := range sysBusPciDevEntries {
		pdname := sysBusPciDevEntry.Name()
		pciDevPath := filepath.Join(sysBusPciDevPath, pdname)
		trace("creating %q\n", pciDevPath)

		// from sysfs layout, we know already we know these are symlinks
		target, err := os.Readlink(pciDevPath)
		if err != nil {
			return err
		}

		if err := os.Symlink(target, filepath.Join(buildDir, pciDevPath)); err != nil {
			return err
		}
	}
	return nil
}

func copyPseudoFiles(buildDir, srcPath string, names []string) error {
	for _, pseudoFile := range names {
		path := filepath.Join(srcPath, pseudoFile)
		if err := copyPseudoFile(path, filepath.Join(buildDir, path)); err != nil {
			return err
		}
	}
	return nil
}
