package sbx

import (
	"reflect"
	"testing"
)

func TestPack(t *testing.T) {
	got := Pack("/k/.build/kit", "/k/.build/kit.zip")
	want := []string{"kit", "pack", "/k/.build/kit", "-o", "/k/.build/kit.zip"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Pack() = %v, want %v", got, want)
	}
}

func TestCreateRunWithVolumes(t *testing.T) {
	got := CreateRun("box", "/k/.build/kit.zip", []string{"/a", "/b"})
	want := []string{"run", "--name", "box", "--kit", "/k/.build/kit.zip", "claude", "/a", "/b"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CreateRun() = %v, want %v", got, want)
	}
}

func TestCreateRunDefaultsVolumeToCwd(t *testing.T) {
	got := CreateRun("box", "/k/.build/kit.zip", nil)
	want := []string{"run", "--name", "box", "--kit", "/k/.build/kit.zip", "claude", "."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CreateRun() = %v, want %v", got, want)
	}
}

func TestRun(t *testing.T) {
	got := Run("box")
	want := []string{"run", "box"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Run() = %v, want %v", got, want)
	}
}

func TestRemove(t *testing.T) {
	got := Remove("box")
	want := []string{"remove", "box"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Remove() = %v, want %v", got, want)
	}
}
