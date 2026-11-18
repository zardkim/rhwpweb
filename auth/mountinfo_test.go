package main

import (
	"strings"
	"testing"
)

const sampleMountinfo = `1 0 0:1 / / rw - overlay overlay rw
100 1 253:0 /docker/rhwp/db /data rw,noatime - btrfs /dev/mapper/cachedev_0 rw
101 1 253:0 /@docker/volumes/portainer_data/_data/compose/5/files /files rw,noatime - btrfs /dev/mapper/cachedev_0 rw
102 101 253:0 /문서/공용 /files/mount/공용 ro,noatime - btrfs /dev/mapper/cachedev_0 rw
103 1 8:1 /var/lib/docker/volumes/rhwp_rhwp-auth-data/_data /data2 rw - ext4 /dev/sda1 rw
104 1 8:1 /home/a\040b /files/mount/a\040b rw - ext4 /dev/sda1 rw`

func TestMountinfo(t *testing.T) {
	m := parseMountinfo(sampleMountinfo)
	if len(m) != 6 {
		t.Fatalf("parse %d", len(m))
	}
	if c := classify("db", "/data", m); c.Kind != "bind" || c.Where != "/docker/rhwp/db" || c.Warn != "" {
		t.Fatalf("bind %+v", c)
	}
	if c := classify("files", "/files", m); c.Kind != "portainer" || c.Warn == "" {
		t.Fatalf("portainer %+v", c)
	}
	if c := classify("v", "/data2", m); c.Kind != "volume" || c.Warn == "" {
		t.Fatalf("volume %+v", c)
	}
	if c := classify("x", "/nothere", m); c.Mounted || c.Warn == "" {
		t.Fatalf("none %+v", c)
	}
	if c := classify("ro", "/files/mount/공용", m); !c.RO {
		t.Fatalf("ro %+v", c)
	}
	if m[5].Target != "/files/mount/a b" {
		t.Fatalf("unescape %q", m[5].Target)
	}
}

func TestVolumeWarn(t *testing.T) {
	w := volumeWarn("/@syno/@docker/volumes/rhwpweb_rhwp-files/_data")
	if !strings.Contains(w, "rhwpweb_rhwp-files") || !strings.Contains(w, "프로젝트 이름: rhwpweb") || !strings.Contains(w, "다시 만들어야") {
		t.Fatal(w)
	}
	if w := volumeWarn("/var/lib/docker/volumes/other/_data"); !strings.Contains(w, "'other'") || strings.Contains(w, "프로젝트") {
		t.Fatal(w)
	}
}
