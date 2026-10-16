package privateadmission

import (
	"bufio"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestPrivateAdmissionCanonicalScopeAndGoroutines(t *testing.T) {
	base := t.TempDir()
	project := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(project, alias); err != nil {
		t.Fatal(err)
	}
	path, err := ProjectPath(project, base)
	if err != nil {
		t.Fatal(err)
	}
	other, err := ProjectPath(alias, base)
	if err != nil || other != path {
		t.Fatal("physical project aliases split admission", err)
	}
	otherProject := t.TempDir()
	different, err := ProjectPath(otherProject, base)
	if err != nil || different == path {
		t.Fatal("distinct projects share admission", err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- With(context.Background(), path, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if err = With(ctx, other, func() error { t.Error("concurrent alias acquired admission"); return nil }); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err = With(context.Background(), different, func() error { return nil }); err != nil {
		t.Fatal("unrelated project blocked", err)
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if err = With(context.Background(), path, func() error { return nil }); err != nil {
		t.Fatal("admission not released", err)
	}
}
func TestPrivateAdmissionCrossProcess(t *testing.T) {
	path, err := ProjectPath(t.TempDir(), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "-test.run=^TestPrivateAdmissionProcessChild$")
	cmd.Env = append(os.Environ(), "DATATUG_ADMISSION_CHILD="+path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	scanner := bufio.NewScanner(out)
	acquired := make(chan bool, 1)
	err = With(context.Background(), path, func() error {
		if e := cmd.Start(); e != nil {
			return e
		}
		if !scanner.Scan() || scanner.Text() != "ready" {
			t.Fatal("child did not start")
		}
		go func() { acquired <- scanner.Scan() && scanner.Text() == "entered" }()
		select {
		case <-acquired:
			t.Fatal("second process bypassed native admission")
		case <-time.After(60 * time.Millisecond):
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case ok := <-acquired:
		if !ok {
			t.Fatal("child failed admission after release")
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("child admission timed out")
	}
	if err = in.Close(); err != nil {
		t.Fatal(err)
	}
	if err = cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
func TestPrivateAdmissionProcessChild(t *testing.T) {
	path := os.Getenv("DATATUG_ADMISSION_CHILD")
	if path == "" {
		t.Skip("child only")
	}
	if _, err := os.Stdout.WriteString("ready\n"); err != nil {
		t.Fatal(err)
	}
	if err := With(context.Background(), path, func() error {
		if _, err := os.Stdout.WriteString("entered\n"); err != nil {
			return err
		}
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
func TestPrivateAdmissionRejectsUnsafePaths(t *testing.T) {
	if _, err := ProjectPath("/does/not/exist", t.TempDir()); err == nil {
		t.Fatal("missing project accepted")
	}
	project := t.TempDir()
	if _, err := ProjectPath(project, project); err == nil {
		t.Fatal("private project overlap accepted")
	}
	if err := With(context.Background(), "relative", func() error { return nil }); err == nil {
		t.Fatal("relative lock accepted")
	}
	base := t.TempDir()
	if err := os.Symlink(t.TempDir(), filepath.Join(base, "detective")); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectPath(project, base); err == nil {
		t.Fatal("private lock symlink accepted")
	}
}

func TestPrivateAdmissionCaseAliasRefusesSplitLock(t *testing.T) {
	project := filepath.Join(t.TempDir(), "CanonicalProject")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(filepath.Dir(project), "canonicalproject")
	canonicalInfo, err := os.Stat(project)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Stat(alias)
	if errors.Is(err, os.ErrNotExist) {
		t.Skip("filesystem does not support case aliases")
	}
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(canonicalInfo, aliasInfo) {
		t.Fatal("case fixture identifies a different directory")
	}
	base := t.TempDir()
	path, err := ProjectPath(project, base)
	if err != nil {
		t.Fatal(err)
	}
	err = With(context.Background(), path, func() error {
		done := make(chan error, 1)
		go func() { _, err := ProjectPath(alias, base); done <- err }()
		if err := <-done; err == nil {
			t.Error("goroutine case alias manufactured another lock")
		}
		binary, err := os.Executable()
		if err != nil {
			return err
		}
		cmd := exec.Command(binary, "-test.run=^TestPrivateAdmissionCaseAliasChild$")
		cmd.Env = append(os.Environ(), "DATATUG_CASE_PROJECT="+alias, "DATATUG_CASE_PRIVATE="+base)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("child case alias probe failed: %s %v", output, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
func TestPrivateAdmissionCaseAliasChild(t *testing.T) {
	project := os.Getenv("DATATUG_CASE_PROJECT")
	if project == "" {
		t.Skip("child only")
	}
	if _, err := ProjectPath(project, os.Getenv("DATATUG_CASE_PRIVATE")); err == nil {
		t.Fatal("case alias manufactured cross-process lock")
	}
}
