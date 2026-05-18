package session

import "testing"

func TestApplyDefaults_TTY(t *testing.T) {
	o := Opts{Mode: ModeTTY}
	o.ApplyDefaults()
	if o.Profile != "default" {
		t.Errorf("Profile: want %q, got %q", "default", o.Profile)
	}
	if o.AutoRemove {
		t.Error("AutoRemove should be false for TTY")
	}
	if o.Yolo {
		t.Error("Yolo should be false for TTY")
	}
}

func TestApplyDefaults_Task(t *testing.T) {
	o := Opts{Mode: ModeTask}
	o.ApplyDefaults()
	if !o.AutoRemove {
		t.Error("AutoRemove should be true for Task by default")
	}
}

func TestApplyDefaults_Background(t *testing.T) {
	o := Opts{Mode: ModeBackground}
	o.ApplyDefaults()
	if o.AutoRemove {
		t.Error("AutoRemove should be false for Background")
	}
}

func TestApplyDefaults_RespectsExplicitProfile(t *testing.T) {
	o := Opts{Mode: ModeTask, Profile: "high"}
	o.ApplyDefaults()
	if o.Profile != "high" {
		t.Errorf("explicit profile must not be overridden: got %q", o.Profile)
	}
}
