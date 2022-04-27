// Copyright Elasticsearch B.V. and/or licensed to Elasticsearch B.V. under one
// or more contributor license agreements. Licensed under the Elastic License;
// you may not use this file except in compliance with the Elastic License.

//go:build mage
// +build mage

package main

import (
	"os"
	"strings"

	"github.com/hashicorp/go-multierror"
	"github.com/magefile/mage/mg"
	"github.com/magefile/mage/sh"
)

const (
	goLintRepo        = "golang.org/x/lint/golint"
	goLicenserRepo    = "github.com/elastic/go-licenser"
	goProtocGenGo     = "google.golang.org/protobuf/cmd/protoc-gen-go@v1.28"
	goProtocGenGoGRPC = "google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.2"
)

// Aliases for commands required by master makefile
var Aliases = map[string]interface{}{
	"prepare": Prepare.All,
	"update":  Update.All,
	"fmt":     Format.All,
	"format":  Format.All,
	"check":   Check.All,
}

// Prepare tasks related to bootstrap the environment or get information about the environment.
type Prepare mg.Namespace

// Update updates the generated GRPC code.
type Update mg.Namespace

// Format automatically format the code.
type Format mg.Namespace

// Check namespace contains tasks related check the actual code quality.
type Check mg.Namespace

// InstallGoLicenser install go-licenser to check license of the files.
func (Prepare) InstallGoLicenser() error {
	return GoGet(goLicenserRepo)
}

// InstallGoLint for the code.
func (Prepare) InstallGoLint() error {
	return GoGet(goLintRepo)
}

// All runs prepare:installGoLicenser and prepare:installGoLint.
func (Prepare) All() {
	mg.SerialDeps(Prepare.InstallGoLicenser, Prepare.InstallGoLint)
}

// Prepare installs the required GRPC tools for generation to occur.
func (Update) Prepare() error {
	if err := GoInstall(goProtocGenGo); err != nil {
		return err
	}
	return GoInstall(goProtocGenGoGRPC)
}

// Generate generates the GRPC code.
func (Update) Generate() error {
	defer mg.SerialDeps(Format.All)
	return sh.RunV(
		"protoc",
		"--go_out=pkg/proto", "--go_opt=paths=source_relative",
		"--go-grpc_out=pkg/proto", "--go-grpc_opt=paths=source_relative",
		"elastic-agent-client.proto")
}

// All runs update:prepare then update:generate.
func (Update) All() {
	mg.SerialDeps(Update.Prepare, Update.Generate)
}

// All format automatically all the codes.
func (Format) All() {
	mg.SerialDeps(Format.License)
}

// License applies the right license header.
func (Format) License() error {
	mg.Deps(Prepare.InstallGoLicenser)
	return combineErr(
		sh.RunV("go-licenser", "-license", "Elastic"),
		sh.RunV("go-licenser", "-license", "Elastic", "-ext", ".proto"),
	)
}

// All run all the code checks.
func (Check) All() {
	mg.SerialDeps(Check.License, Check.GoLint)
}

// GoLint run the code through the linter.
func (Check) GoLint() error {
	mg.Deps(Prepare.InstallGoLint)
	packagesString, err := sh.Output("go", "list", "./...")
	if err != nil {
		return err
	}

	packages := strings.Split(packagesString, "\n")
	for _, pkg := range packages {
		if strings.Contains(pkg, "/vendor/") {
			continue
		}

		if e := sh.RunV("golint", "-set_exit_status", pkg); e != nil {
			err = multierror.Append(err, e)
		}
	}

	return err
}

// License makes sure that all the Golang files have the appropriate license header.
func (Check) License() error {
	mg.Deps(Prepare.InstallGoLicenser)
	// exclude copied files until we come up with a better option
	return combineErr(
		sh.RunV("go-licenser", "-d", "-license", "Elastic"),
	)
}

// GoGet fetch a remote dependencies.
func GoGet(link string) error {
	_, err := sh.Exec(map[string]string{"GO111MODULE": "off"}, os.Stdout, os.Stderr, "go", "get", link)
	return err
}

// GoInstall installs a remote dependencies.
func GoInstall(link string) error {
	_, err := sh.Exec(map[string]string{}, os.Stdout, os.Stderr, "go", "install", link)
	return err
}

func combineErr(errors ...error) error {
	var e error
	for _, err := range errors {
		if err == nil {
			continue
		}
		e = multierror.Append(e, err)
	}
	return e
}
