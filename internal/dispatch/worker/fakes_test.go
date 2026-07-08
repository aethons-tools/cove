package worker

import "context"

// fakeGit records calls and returns configured values.
type fakeGit struct {
	calls     []string
	remoteHas bool
	changes   bool
	differs   bool
	sha       string
	ensureErr error
	failOn    string // method name to return an error from
}

func (f *fakeGit) rec(m string) { f.calls = append(f.calls, m) }
func (f *fakeGit) err(m string) error {
	if f.failOn == m {
		return context.Canceled
	}
	return nil
}
func (f *fakeGit) EnsureClean(_ context.Context, _, _ string) error {
	f.rec("EnsureClean")
	if f.ensureErr != nil {
		return f.ensureErr
	}
	return f.err("EnsureClean")
}
func (f *fakeGit) Sync(_ context.Context, _, b string) error {
	f.rec("Sync:" + b)
	return f.err("Sync")
}
func (f *fakeGit) RemoteHasBranch(_ context.Context, _, _ string) (bool, error) {
	f.rec("RemoteHasBranch")
	return f.remoteHas, f.err("RemoteHasBranch")
}
func (f *fakeGit) NewBranch(_ context.Context, _, b, _ string) error {
	f.rec("NewBranch:" + b)
	return f.err("NewBranch")
}
func (f *fakeGit) HasChanges(_ context.Context, _ string) (bool, error) {
	f.rec("HasChanges")
	return f.changes, f.err("HasChanges")
}
func (f *fakeGit) DiffersFrom(_ context.Context, _, _ string) (bool, error) {
	f.rec("DiffersFrom")
	return f.differs, f.err("DiffersFrom")
}
func (f *fakeGit) Commit(_ context.Context, _, _ string) (string, error) {
	f.rec("Commit")
	return f.sha, f.err("Commit")
}
func (f *fakeGit) Push(_ context.Context, _, b string) error {
	f.rec("Push:" + b)
	return f.err("Push")
}
func (f *fakeGit) Head(_ context.Context, _ string) (string, error) {
	f.rec("Head")
	return f.sha, f.err("Head")
}

// fakeCodeHost records whether OpenPR was called and returns configured values.
type fakeCodeHost struct {
	url    string
	err    error
	opened bool
}

func (f *fakeCodeHost) OpenPR(_ context.Context, _, _, _, _, _ string) (string, error) {
	f.opened = true
	return f.url, f.err
}
