//go:build linux

package main

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRouteSupervisorOwnershipRejectsContenderAndPermitsTakeover(t *testing.T) {
	config := privateSupervisorOwnershipConfig(t)
	first, err := acquireRouteSupervisorOwnership(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRouteSupervisorOwnership(config); err == nil {
		t.Fatal("concurrent route supervisor acquired active ownership")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := acquireRouteSupervisorOwnership(config)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()

	info, err := os.Lstat(routeSupervisorOwnershipPath(config.StateFile))
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		t.Fatal("route supervisor ownership file is not private and stable")
	}
}

func TestRouteManagerOwnershipRejectsContenderAndPermitsTakeover(t *testing.T) {
	config := privateSupervisorOwnershipConfig(t)
	first, err := acquireRouteManagerOwnership(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRouteManagerOwnership(config); err == nil {
		t.Fatal("concurrent route manager acquired active ownership")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	replacement, err := acquireRouteManagerOwnership(config)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()

	info, err := os.Lstat(routeManagerOwnershipPath(config.StateFile))
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		stat.Nlink != 1 || stat.Uid != uint32(os.Geteuid()) {
		t.Fatal("route manager ownership file is not private and stable")
	}
}

func TestRouteManagerAndSupervisorOwnershipAreIndependent(t *testing.T) {
	config := privateSupervisorOwnershipConfig(t)
	manager, err := acquireRouteManagerOwnership(config)
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	supervisor, err := acquireRouteSupervisorOwnership(config)
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()

	if routeManagerOwnershipPath(config.StateFile) == routeSupervisorOwnershipPath(config.StateFile) {
		t.Fatal("route manager and supervisor share an ownership path")
	}
}

func TestRouteSupervisorOwnershipRejectsUnsafeFiles(t *testing.T) {
	for _, test := range []struct {
		name  string
		setup func(*testing.T, string)
	}{
		{
			name: "symbolic link",
			setup: func(t *testing.T, path string) {
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "weakened mode",
			setup: func(t *testing.T, path string) {
				if err := os.WriteFile(path, nil, 0o644); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "hard link",
			setup: func(t *testing.T, path string) {
				target := filepath.Join(filepath.Dir(path), "target")
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, path); err != nil {
					t.Fatal(err)
				}
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := privateSupervisorOwnershipConfig(t)
			test.setup(t, routeSupervisorOwnershipPath(config.StateFile))
			if ownership, err := acquireRouteSupervisorOwnership(config); err == nil {
				_ = ownership.Close()
				t.Fatal("route supervisor accepted an unsafe ownership file")
			}
		})
	}
}

func TestRouteSupervisorOwnershipRejectsControlSocketCollision(t *testing.T) {
	config := privateSupervisorOwnershipConfig(t)
	config.ControlSocket = routeSupervisorOwnershipPath(config.StateFile)
	if ownership, err := acquireRouteSupervisorOwnership(config); err == nil {
		_ = ownership.Close()
		t.Fatal("route supervisor accepted an ownership and control path collision")
	}
}

func TestRouteManagerOwnershipRejectsControlSocketCollision(t *testing.T) {
	config := privateSupervisorOwnershipConfig(t)
	config.ControlSocket = routeManagerOwnershipPath(config.StateFile)
	if ownership, err := acquireRouteManagerOwnership(config); err == nil {
		_ = ownership.Close()
		t.Fatal("route manager accepted an ownership and control path collision")
	}
}

func TestRouteSupervisorOwnershipRejectsWeakenedWorkspace(t *testing.T) {
	config := privateSupervisorOwnershipConfig(t)
	if err := os.Chmod(filepath.Dir(config.StateFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if ownership, err := acquireRouteSupervisorOwnership(config); err == nil {
		_ = ownership.Close()
		t.Fatal("route supervisor accepted a non-private ownership workspace")
	}
}

func privateSupervisorOwnershipConfig(t *testing.T) routeSupervisorConfig {
	t.Helper()
	workspace := t.TempDir()
	if err := os.Chmod(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	return routeSupervisorConfig{
		ControlSocket: filepath.Join(workspace, "control.sock"),
		StateFile:     filepath.Join(workspace, "state.json"),
	}
}
