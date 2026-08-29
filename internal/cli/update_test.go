package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/thewelshrich/schooner/internal/selfupdate"
)

func TestUpdateCommandCheckJSONContract(t *testing.T) {
	var mode selfupdate.Mode
	options := &globalOptions{output: "json", selfUpdate: func(_ context.Context, selected selfupdate.Mode) (selfupdate.Result, error) {
		mode = selected
		return selfupdate.Result{InstalledVersion: "v0.2.0", AvailableVersion: "v0.3.0", InstallationMethod: selfupdate.MethodDirect, Action: selfupdate.ActionUpdateAvailable, Guidance: "Run `schooner update` to install it."}, nil
	}}
	var output bytes.Buffer
	command := newUpdateCommand(Streams{}, options)
	command.SetOut(&output)
	command.SetArgs([]string{"--check"})
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if mode != selfupdate.ModeCheck {
		t.Fatalf("mode = %q", mode)
	}
	var document updateDocument
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != "1" || document.Action != selfupdate.ActionUpdateAvailable || document.InstallationMethod != selfupdate.MethodDirect {
		t.Fatalf("document = %#v", document)
	}
}

func TestUpdateCommandApplyKeepsOwnerGuidanceSuccessful(t *testing.T) {
	options := &globalOptions{output: "human", selfUpdate: func(_ context.Context, selected selfupdate.Mode) (selfupdate.Result, error) {
		if selected != selfupdate.ModeApply {
			t.Fatalf("mode = %q", selected)
		}
		return selfupdate.Result{InstalledVersion: "v0.2.0", AvailableVersion: "v0.3.0", InstallationMethod: selfupdate.MethodHomebrew, Action: selfupdate.ActionUsePackageManager, Guidance: "Run brew."}, nil
	}}
	var output bytes.Buffer
	command := newUpdateCommand(Streams{}, options)
	command.SetOut(&output)
	if err := command.ExecuteContext(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "Homebrew owns this installation") || !strings.Contains(output.String(), "Run brew.") {
		t.Fatalf("output = %q", output.String())
	}
}

func TestSelfUpdateErrorsUseStandardJSONContext(t *testing.T) {
	err := &selfupdate.Error{Code: selfupdate.CodeOwnershipRefused, Message: "not owned", Context: map[string]string{"action": selfupdate.ActionRefused, "installation_method": selfupdate.MethodUnknown}}
	var output bytes.Buffer
	printError(&output, executionError{cause: err}, "json", nil)
	var document struct {
		Error struct {
			Code    string            `json:"code"`
			Context map[string]string `json:"context"`
		} `json:"error"`
	}
	if decodeErr := json.Unmarshal(output.Bytes(), &document); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if document.Error.Code != string(selfupdate.CodeOwnershipRefused) || document.Error.Context["action"] != selfupdate.ActionRefused {
		t.Fatalf("document = %#v", document)
	}
}

func TestAutomaticUpdateNoticeIsStderrOnlyAndNonFatal(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("SCHOONER_NO_UPDATE_CHECK", "")
	var stderr bytes.Buffer
	options := &globalOptions{build: BuildInfo{Version: "v0.2.0"}, output: "human", selfUpdate: func(_ context.Context, mode selfupdate.Mode) (selfupdate.Result, error) {
		if mode != selfupdate.ModeAutomatic {
			t.Fatalf("mode = %q", mode)
		}
		return selfupdate.Result{InstalledVersion: "v0.2.0", AvailableVersion: "v0.3.0", InstallationMethod: selfupdate.MethodDirect, Action: selfupdate.ActionUpdateAvailable, Guidance: "Run `schooner update` to install it."}, nil
	}}
	root := &cobra.Command{Use: "schooner"}
	command := &cobra.Command{Use: "doctor"}
	root.AddCommand(command)
	maybeWriteAutomaticUpdateNotice(t.Context(), []string{"doctor"}, command, Streams{Err: &stderr, OutIsTerminal: true, ErrIsTerminal: true}, options)
	if !strings.Contains(stderr.String(), "Update available: v0.2.0 → v0.3.0") {
		t.Fatalf("stderr = %q", stderr.String())
	}

	options.selfUpdate = func(context.Context, selfupdate.Mode) (selfupdate.Result, error) {
		return selfupdate.Result{}, errors.New("offline")
	}
	stderr.Reset()
	maybeWriteAutomaticUpdateNotice(t.Context(), []string{"doctor"}, command, Streams{Err: &stderr, OutIsTerminal: true, ErrIsTerminal: true}, options)
	if stderr.Len() != 0 {
		t.Fatalf("automatic failure wrote stderr: %q", stderr.String())
	}
}

func TestAutomaticUpdateEligibilitySuppressesAutomationAndSpecialCommands(t *testing.T) {
	t.Setenv("CI", "")
	t.Setenv("SCHOONER_NO_UPDATE_CHECK", "")
	root := &cobra.Command{Use: "schooner"}
	doctor := &cobra.Command{Use: "doctor"}
	version := &cobra.Command{Use: "version"}
	host := &cobra.Command{Use: "host", Hidden: true}
	hostChild := &cobra.Command{Use: "inspect"}
	host.AddCommand(hostChild)
	root.AddCommand(doctor, version, host)
	streams := Streams{OutIsTerminal: true, ErrIsTerminal: true}
	base := &globalOptions{build: BuildInfo{Version: "v0.2.0"}, output: "human", selfUpdate: func(context.Context, selfupdate.Mode) (selfupdate.Result, error) { return selfupdate.Result{}, nil }}
	if !automaticUpdateEligible([]string{"doctor"}, doctor, streams, base) {
		t.Fatal("ordinary terminal command was not eligible")
	}
	t.Setenv("CI", "1")
	if automaticUpdateEligible([]string{"doctor"}, doctor, streams, base) {
		t.Fatal("CI command was eligible")
	}
	t.Setenv("CI", "")
	t.Setenv("SCHOONER_NO_UPDATE_CHECK", "1")
	if automaticUpdateEligible([]string{"doctor"}, doctor, streams, base) {
		t.Fatal("opted-out command was eligible")
	}
	t.Setenv("SCHOONER_NO_UPDATE_CHECK", "")
	for _, test := range []struct {
		name    string
		args    []string
		command *cobra.Command
		mutate  func(*globalOptions, *Streams)
	}{
		{name: "version", command: version},
		{name: "hidden host", command: hostChild},
		{name: "help", args: []string{"doctor", "--help"}, command: doctor},
		{name: "json", command: doctor, mutate: func(options *globalOptions, _ *Streams) { options.output = "json" }},
		{name: "no input", command: doctor, mutate: func(options *globalOptions, _ *Streams) { options.noInput = true }},
		{name: "nonterminal", command: doctor, mutate: func(_ *globalOptions, streams *Streams) { streams.ErrIsTerminal = false }},
		{name: "prerelease", command: doctor, mutate: func(options *globalOptions, _ *Streams) { options.build.Version = "v0.2.0-rc.1" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			optionsCopy, streamsCopy := *base, streams
			if test.mutate != nil {
				test.mutate(&optionsCopy, &streamsCopy)
			}
			if automaticUpdateEligible(test.args, test.command, streamsCopy, &optionsCopy) {
				t.Fatal("suppressed command was eligible")
			}
		})
	}
}
