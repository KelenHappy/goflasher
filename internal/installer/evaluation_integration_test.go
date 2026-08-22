package installer_test

import (
	"context"
	"os"
	"strconv"
	"testing"

	"github.com/goflasher/goflasher/internal/image"
	"github.com/goflasher/goflasher/internal/installer"
	installeriso "github.com/goflasher/goflasher/internal/installer/iso"
	"github.com/goflasher/goflasher/internal/verify"
)

func TestAuthorizedWindowsEvaluationISO(t *testing.T) {
	isoPath := os.Getenv("GOFLASHER_WINDOWS_EVAL_ISO")
	if isoPath == "" {
		t.Skip("GOFLASHER_WINDOWS_EVAL_ISO is not provisioned by an authorized source")
	}
	info, err := image.Detect(isoPath)
	if err != nil {
		t.Fatal(err)
	}
	defer info.CloseSource()
	if kind, err := image.ClassifyContext(context.Background(), info); err != nil || kind != image.WindowsInstallerISO {
		t.Fatalf("classification=%s error=%v", kind, err)
	}
	r, size, lease, err := info.RetainedReaderAt()
	if err != nil {
		t.Fatal(err)
	}
	fs, err := installeriso.New(r, size, lease)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	targetSize := uint64(16 << 30)
	if value := os.Getenv("GOFLASHER_EVAL_USB_BYTES"); value != "" {
		targetSize, err = strconv.ParseUint(value, 10, 64)
		if err != nil {
			t.Fatal(err)
		}
	}
	splitter := installer.NewNativeWIMSplitter()
	plan, err := installer.NewBuildPlan(context.Background(), r, uint64(size), fs.Manifest(), installer.PlanOptions{SourceIdentity: "authorized-evaluation", TargetSize: targetSize, TemporarySpace: targetSize, SplitPreflight: splitter.Preflight})
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.CreateTemp("", "goflasher-evaluation-usb-*.img")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(target.Name())
	defer target.Close()
	if err := target.Truncate(int64(targetSize)); err != nil {
		t.Fatal(err)
	}
	result, err := (installer.Executor{Splitter: splitter}).Execute(context.Background(), plan, r, target)
	if err != nil || !result.Complete {
		t.Fatalf("execution complete=%t error=%v", result.Complete, err)
	}
	verified, err := verify.VerifyInstaller(context.Background(), verify.RawTarget{Reader: target, Size: targetSize}, result.VerificationManifest, verify.InstallerOptions{SplitWIMPolicySize: plan.SplitWIMPolicySize(), RequireSplitWIM: plan.InstallStrategy() == installer.SplitWIM})
	if err != nil || verified.FilesVerified == 0 {
		t.Fatalf("verification=%+v error=%v", verified, err)
	}
}
