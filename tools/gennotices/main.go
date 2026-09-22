// Command gennotices regenerates THIRD-PARTY-NOTICES.txt from the Go module
// cache.
//
// Scope is every module linked into the released artefacts — the modules
// reachable from ./cmd/..., which is what ends up inside the opentams and
// tamsctl binaries and the published container images. Build- and test-only
// modules are excluded because they are never redistributed.
//
// The set is the union across every platform we release for, not the one the
// generator happens to run on. Build constraints make it platform-specific:
// github.com/prometheus/procfs is linked on linux and not on darwin, so
// generating on a Mac would omit a module that ships inside the linux
// container images.
//
// For each module the tool reads the licence file shipped in the module
// cache, classifies it, and pulls out the copyright notices. Modules are then
// grouped by licence family, one section each.
//
// Every component reproduces its own licence file verbatim, because the notice
// that has to travel with it is specific to that module. The exception is an
// Apache-2.0 file whose terms match this repository's LICENSE: those refer to
// LICENSE rather than repeating ~10 KB of identical text some thirty times.
// The match is checked, not assumed, so a dependency shipping a modified
// Apache licence is still reproduced in full.
//
//	go run ./tools/gennotices          # rewrite THIRD-PARTY-NOTICES.txt
//	go run ./tools/gennotices -check   # fail if the committed files are stale
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	outFile     = "THIRD-PARTY-NOTICES.txt"
	ownModule   = "github.com/amagioss/opentams"
	copyrightCO = "Copyright © 2026 Amagi Media Labs Limited"
)

// releaseTargets are the GOOS/GOARCH pairs .goreleaser.yml builds: linux for
// both binaries and the container images, darwin for the tamsctl archives.
// Keep in step with the builds section there.
var releaseTargets = []struct{ goos, goarch string }{
	{"linux", "amd64"},
	{"linux", "arm64"},
	{"darwin", "amd64"},
	{"darwin", "arm64"},
}

// familyOrder fixes the order the licence families appear in, both in the
// index table and as files on disk. Alphabetical would put 0BSD first, which
// buries the two families that carry almost every module.
var familyOrder = []string{"MIT", "Apache-2.0", "BSD-3-Clause", "BSD-2-Clause", "0BSD", "ISC", "MPL-2.0"}

// licenseDoc is one licence a module is distributed under. A module usually
// has exactly one; a dual-licensed module has several, either as separate
// files or as several licences inside a single file.
type licenseDoc struct {
	file   string
	family string
	text   string
	// sha256 of the licence file as shipped in the module cache. It anchors
	// this entry to an exact upstream file, so a silent relicensing upstream
	// shows up as a changed digest rather than passing unnoticed.
	sha256 string
	// sameAsRepoLicense is set when an Apache-2.0 licence file carries the
	// same terms as this repository's own LICENSE. Only then may the entry
	// point at LICENSE instead of reproducing its text.
	sameAsRepoLicense bool
	combined          bool
}

type module struct {
	Path    string
	Version string
	Dir     string

	licenses   []licenseDoc
	copyrights []string
	noticeText string

	// inheritedFrom names the parent module a copyright notice was taken
	// from, when this module ships none of its own.
	inheritedFrom string
}

// entry pairs a module with one of its licences. A dual-licensed module
// produces one entry per licence and so is listed under each family it is
// actually distributed under.
type entry struct {
	mod module
	lic licenseDoc
}

func main() {
	check := flag.Bool("check", false, "verify the committed file is up to date instead of writing it")
	flag.Parse()

	root, err := repoRoot()
	if err != nil {
		fatal(err)
	}

	mods, err := linkedModules(root)
	if err != nil {
		fatal(err)
	}
	if len(mods) == 0 {
		fatal(fmt.Errorf("no third-party modules resolved from ./cmd/... — is the module cache populated?"))
	}

	// An Apache-2.0 component may only refer to LICENSE for its terms when
	// its licence file actually carries those terms, so LICENSE is the
	// reference every Apache component is checked against.
	repoLicense, err := os.ReadFile(filepath.Join(root, "LICENSE"))
	if err != nil {
		fatal(fmt.Errorf("reading LICENSE: %w", err))
	}
	repoApacheTerms := apacheTerms(string(repoLicense))
	if repoApacheTerms == "" {
		fatal(fmt.Errorf("LICENSE does not look like the Apache License 2.0"))
	}

	for i := range mods {
		if err := mods[i].classify(repoApacheTerms); err != nil {
			fatal(fmt.Errorf("%s: %w", mods[i].Path, err))
		}
	}

	inheritCopyrights(mods)

	byFamily := map[string][]entry{}
	for _, m := range mods {
		for _, lic := range m.licenses {
			byFamily[lic.family] = append(byFamily[lic.family], entry{mod: m, lic: lic})
		}
	}

	var unknown []string
	for fam, list := range byFamily {
		if !knownFamily(fam) {
			for _, e := range list {
				unknown = append(unknown, e.mod.Path+" ("+fam+")")
			}
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		fatal(fmt.Errorf("unclassified licences, add a template or fix detection:\n  %s",
			strings.Join(unknown, "\n  ")))
	}

	for fam := range byFamily {
		list := byFamily[fam]
		sort.Slice(list, func(i, j int) bool { return list[i].mod.Path < list[j].mod.Path })
	}

	want := render(byFamily, len(mods))
	path := filepath.Join(root, outFile)

	if *check {
		got, err := os.ReadFile(path)
		if err != nil {
			fatal(fmt.Errorf("%s is missing, run `make notices`: %w", outFile, err))
		}
		if string(got) != want {
			fatal(fmt.Errorf("%s is stale, run `make notices`", outFile))
		}
		fmt.Printf("%s is up to date (%d modules, %d licence families)\n", outFile, len(mods), len(byFamily))
		return
	}
	if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s: %d modules across %d licence families, %d KiB\n",
		outFile, len(mods), len(byFamily), len(want)/1024)
}

func repoRoot() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("locating module root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// linkedModules returns the third-party modules reachable from ./cmd/... on
// any platform we release for, deduplicated and sorted. Packages from the
// standard library carry no module and are skipped, as is our own module.
func linkedModules(root string) ([]module, error) {
	seen := map[string]module{}
	for _, t := range releaseTargets {
		if err := listForTarget(root, t.goos, t.goarch, seen); err != nil {
			return nil, err
		}
	}

	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
}

func listForTarget(root, goos, goarch string, seen map[string]module) error {
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/...")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch, "CGO_ENABLED=0")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("go list -deps for %s/%s: %w: %s", goos, goarch, err, stderr.String())
	}

	type pkg struct {
		Module *struct {
			Path    string
			Version string
			Dir     string
		}
	}

	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return fmt.Errorf("decoding go list output for %s/%s: %w", goos, goarch, err)
		}
		if p.Module == nil || p.Module.Dir == "" {
			continue
		}
		if p.Module.Path == ownModule || strings.HasPrefix(p.Module.Path, ownModule+"/") {
			continue
		}
		seen[p.Module.Path] = module{Path: p.Module.Path, Version: p.Module.Version, Dir: p.Module.Dir}
	}
	return nil
}

var (
	// Matches LICENSE, LICENCE, COPYING and their suffixed variants
	// (LICENSE.txt, LICENSE-MIT, LICENSE.libyaml). The suffixed forms are how
	// a dual-licensed module separates its licences into two files, so
	// stopping at the bare name would silently drop one of them.
	licenseNameRE = regexp.MustCompile(`(?i)^(LICEN[CS]E|COPYING)([._-][A-Za-z0-9._-]*)?$`)
	noticeNameRE  = regexp.MustCompile(`(?i)^NOTICE(\.(txt|md))?$`)
	spaceRE       = regexp.MustCompile(`\s+`)
)

func (m *module) classify(repoApacheTerms string) error {
	dirEntries, err := os.ReadDir(m.Dir)
	if err != nil {
		return fmt.Errorf("reading module dir: %w", err)
	}

	for _, e := range dirEntries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case licenseNameRE.MatchString(name):
			b, err := os.ReadFile(filepath.Join(m.Dir, name))
			if err != nil {
				return err
			}
			text := string(b)
			fams := detectFamilies(text)
			if len(fams) == 0 {
				return fmt.Errorf("unrecognised licence text in %s", name)
			}
			for _, fam := range fams {
				sum := sha256.Sum256(b)
				m.licenses = append(m.licenses, licenseDoc{
					file:     name,
					family:   fam,
					text:     text,
					sha256:   hex.EncodeToString(sum[:]),
					combined: len(fams) > 1,
					// A file covering several licences is never the same as
					// LICENSE, so it is always reproduced.
					sameAsRepoLicense: len(fams) == 1 && fam == "Apache-2.0" &&
						apacheTerms(text) == repoApacheTerms,
				})
			}
		case noticeNameRE.MatchString(name) && m.noticeText == "":
			b, err := os.ReadFile(filepath.Join(m.Dir, name))
			if err != nil {
				return err
			}
			m.noticeText = strings.TrimRight(string(b), "\n")
		}
	}
	if len(m.licenses) == 0 {
		return fmt.Errorf("no licence file found in %s", m.Dir)
	}

	sort.Slice(m.licenses, func(i, j int) bool {
		if m.licenses[i].family != m.licenses[j].family {
			return familyRank(m.licenses[i].family) < familyRank(m.licenses[j].family)
		}
		return m.licenses[i].file < m.licenses[j].file
	})
	m.copyrights = m.findCopyrights()
	return nil
}

// detectFamilies returns every licence family present in one text, in
// familyOrder. Most files carry exactly one. Some do not: gopkg.in/yaml.v3
// ships a single LICENSE that opens "This project is covered by two different
// licenses: MIT and Apache" and then reproduces both, because the files ported
// from libyaml keep their original MIT terms. Returning only the first match
// would drop one of the two licences the module is actually under.
func detectFamilies(text string) []string {
	n := normalize(text)
	has := func(s string) bool { return strings.Contains(n, s) }

	var fams []string
	add := func(f string) { fams = append(fams, f) }

	if has("apache license") && has("version 2.0") {
		add("Apache-2.0")
	}
	if has("permission is hereby granted, free of charge") {
		add("MIT")
	}
	if has("permission to use, copy, modify, and/or distribute this software for any purpose") {
		// 0BSD and ISC share this opening. ISC keeps a copyright-retention
		// condition; 0BSD drops every condition.
		if has("provided that the above copyright notice") {
			add("ISC")
		} else {
			add("0BSD")
		}
	}
	if has("redistribution and use in source and binary forms") {
		if has("neither the name") {
			add("BSD-3-Clause")
		} else {
			add("BSD-2-Clause")
		}
	}
	if has("mozilla public license") {
		add("MPL-2.0")
	}

	sort.Slice(fams, func(i, j int) bool { return familyRank(fams[i]) < familyRank(fams[j]) })
	return fams
}

func familyRank(fam string) int {
	for i, f := range familyOrder {
		if f == fam {
			return i
		}
	}
	return len(familyOrder)
}

func knownFamily(fam string) bool {
	for _, f := range familyOrder {
		if f == fam {
			return true
		}
	}
	return false
}

// findCopyrights pulls the copyright notices for a module.
//
// The licence file is the authoritative source for every family except
// Apache-2.0: that licence is a fixed template which asserts no copyright of
// its own, and scanning it only turns up sentences from section 4 that happen
// to contain the word. Apache modules state their copyright in a NOTICE file
// or in source-file headers instead, which is where AWS, Google and the
// Prometheus projects actually put theirs.
func (m *module) findCopyrights() []string {
	for _, lic := range m.licenses {
		if lic.family == "Apache-2.0" {
			continue
		}
		if c := scanCopyrights(lic.text); len(c) > 0 {
			return c
		}
	}
	if c := scanCopyrights(m.noticeText); len(c) > 0 {
		return c
	}
	return m.scanSourceHeaders()
}

// copyrightLineRE matches a line that asserts a copyright, as opposed to prose
// that merely contains the word.
//
// Two things do the work. Anchoring at the start of the line (after any
// comment marker) separates "Copyright 2015 Amazon.com, Inc." from "You may
// add Your own copyright statement to Your modifications". Matching only the
// capitalised word rejects a wrapped prose line that happens to begin with it,
// as gopkg.in/yaml.v3's "copyright staring in 2011 when the project was ported
// over:" does — a notice starts a sentence, so it is always capitalised.
var copyrightLineRE = regexp.MustCompile(`^\s*(//+|#+|\*+|/\*)?\s*(Copyright|COPYRIGHT)\b`)

func scanCopyrights(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !copyrightLineRE.MatchString(line) {
			continue
		}
		// The Apache boilerplate placeholder is not a notice.
		low := strings.ToLower(line)
		if strings.Contains(low, "[yyyy]") || strings.Contains(low, "<year>") ||
			strings.Contains(low, "[name of copyright owner]") {
			continue
		}
		for _, p := range []string{"// ", "//", "# ", "#", "* ", "/* "} {
			if strings.HasPrefix(line, p) {
				line = strings.TrimPrefix(line, p)
				break
			}
		}
		line = strings.TrimSpace(strings.TrimSuffix(line, "*/"))
		if !seen[line] {
			seen[line] = true
			out = append(out, line)
		}
		if len(out) >= 6 {
			break
		}
	}
	return out
}

// inheritCopyrights fills in modules that ship neither a NOTICE nor copyright
// headers — the AWS SDK's many submodules are the case that matters here. Each
// borrows the notice of the longest-prefix parent module that has one, which
// is the project it is actually published by.
func inheritCopyrights(mods []module) {
	byPath := make(map[string][]string, len(mods))
	for _, m := range mods {
		if len(m.copyrights) > 0 {
			byPath[m.Path] = m.copyrights
		}
	}
	for i := range mods {
		if len(mods[i].copyrights) > 0 {
			continue
		}
		best := ""
		for path := range byPath {
			if strings.HasPrefix(mods[i].Path, path+"/") && len(path) > len(best) {
				best = path
			}
		}
		if best != "" {
			mods[i].copyrights = byPath[best]
			mods[i].inheritedFrom = best
		}
	}
}

// scanSourceHeaders looks for a copyright in the comment header of the
// module's Go files. It tries the module root first, then each immediate
// subdirectory: a module like go.mongodb.org/mongo-driver/v2 keeps no Go files
// at its root at all, so a root-only scan would find nothing.
func (m *module) scanSourceHeaders() []string {
	if c := scanDirHeaders(m.Dir); len(c) > 0 {
		return c
	}
	entries, err := os.ReadDir(m.Dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || e.Name() == "testdata" {
			continue
		}
		if c := scanDirHeaders(filepath.Join(m.Dir, e.Name())); len(c) > 0 {
			return c
		}
	}
	return nil
}

func scanDirHeaders(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		head := string(b)
		if len(head) > 2048 {
			head = head[:2048]
		}
		// Only the leading comment block counts; a copyright further down the
		// file belongs to vendored or quoted material, not the module.
		if i := strings.Index(head, "\npackage "); i > 0 {
			head = head[:i]
		}
		if c := scanCopyrights(head); len(c) > 0 {
			return c
		}
	}
	return nil
}

// normalize folds a licence text down to what the comparison cares about:
// case, run-length whitespace, and the typographic quotes and dashes that
// upstream projects use interchangeably with their ASCII equivalents.
var quoteFolder = strings.NewReplacer(
	"“", `"`, "”", `"`, "„", `"`,
	"‘", "'", "’", "'",
	"–", "-", "—", "-",
)

func normalize(s string) string {
	return spaceRE.ReplaceAllString(strings.ToLower(quoteFolder.Replace(s)), " ")
}

// apacheTerms reduces an Apache 2.0 licence file to its operative terms,
// normalized for comparison: everything from the "TERMS AND CONDITIONS"
// heading up to the "END OF TERMS AND CONDITIONS" marker. That drops the
// title block, the appendix, and the trailing marker itself, none of which
// carry terms and all of which upstream projects format differently.
func apacheTerms(text string) string {
	n := normalize(text)
	if i := strings.Index(n, "terms and conditions for use, reproduction, and distribution"); i >= 0 {
		n = n[i:]
	}
	if i := strings.Index(n, "end of terms and conditions"); i > 0 {
		n = n[:i]
	}
	return strings.TrimSpace(n)
}

// render builds the whole THIRD-PARTY-NOTICES file.
//
// The wording, the section headings and the per-component field labels are
// the legal team's template. Everything this tool adds is either a data field
// (SPDX identifier, license file digest) or a one-line statement of fact the
// template has no slot for: an inherited copyright notice, a dual-licensed
// module, the section 4(d) attribution the template asks for in brackets.
func render(byFamily map[string][]entry, total int) string {
	var b strings.Builder
	sep := strings.Repeat("-", 28)

	fmt.Fprintf(&b, "Third Party Software Licenses\n\n")
	fmt.Fprintf(&b, "OpenTAMS uses third-party open-source software components. Each component\nremains subject to its respective copyright and license terms.\n\n")
	fmt.Fprintf(&b, "The following information identifies the applicable third-party components,\nversions, licenses, copyright notices, and license conditions:\n\n")
	fmt.Fprintf(&b, "Generated from the Go module cache by `make notices`. Do not edit by hand.\n")

	for _, fam := range familyOrder {
		list, ok := byFamily[fam]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "\n%s\n\n", sep)
		b.WriteString(renderFamily(fam, list))
	}
	return b.String()
}

func renderFamily(fam string, entries []entry) string {
	var b strings.Builder

	fmt.Fprintf(&b, "%s Components\n\n", familyHeading(fam))

	for _, e := range entries {
		m, lic := e.mod, e.lic
		fmt.Fprintf(&b, "%s\n\n", m.Path)
		fmt.Fprintf(&b, "Module: %s\n", m.Path)
		fmt.Fprintf(&b, "Version: %s\n", m.Version)
		fmt.Fprintf(&b, "License: %s\n", familyField(fam))
		if len(m.copyrights) == 0 {
			fmt.Fprintf(&b, "Copyright: not stated by the module\n")
		}
		for i, c := range m.copyrights {
			label := "Copyright:"
			if i > 0 {
				label = "          "
			}
			fmt.Fprintf(&b, "%s %s\n", label, c)
		}
		if m.inheritedFrom != "" {
			fmt.Fprintf(&b, "           (notice of %s, which publishes this module)\n", m.inheritedFrom)
		}
		fmt.Fprintf(&b, "SPDX-License-Identifier: %s\n", fam)
		fmt.Fprintf(&b, "License file: %s (sha256 %s)\n", lic.file, lic.sha256)
		if len(m.licenses) > 1 {
			fmt.Fprintf(&b, "Dual-licensed under %s; listed under each.\n", strings.Join(licenseFamilies(m), " and "))
		}

		fmt.Fprintf(&b, "\nLicense Terms:\n\n")
		if reproduce(fam, lic) {
			fmt.Fprintf(&b, "%s\n", indent(strings.TrimRight(lic.text, "\n"), "    "))
		} else {
			fmt.Fprintf(&b, "    Licensed under the Apache 2.0 License, version 2.0. See the applicable\n    Apache 2.0 License text in the LICENSE file at the root of this\n    repository.\n")
		}
		if fam == "Apache-2.0" && m.noticeText != "" {
			fmt.Fprintf(&b, "\n    Attribution information from the NOTICE file of this Apache module:\n\n")
			fmt.Fprintf(&b, "%s\n", indent(m.noticeText, "        "))
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}

// reproduce decides whether a component's own licence text is printed.
//
// Everything is reproduced except an Apache-2.0 licence file whose terms match
// this repository's LICENSE. Repeating the Apache text would add roughly 10 KB
// per module for ~30 modules, all of it identical to LICENSE, which already
// travels with every artefact and is what section 4(a) requires. A modified or
// multi-licence Apache file is still reproduced, because then the text is no
// longer the one LICENSE carries.
func reproduce(fam string, lic licenseDoc) bool {
	if fam != "Apache-2.0" {
		return true
	}
	return lic.combined || !lic.sameAsRepoLicense
}

// familyNames carries the two ways each licence has to be named: the section
// heading, and the value of the per-component "License:" field.
var familyNames = map[string]struct{ heading, field string }{
	"MIT":          {"MIT License", "The MIT License"},
	"Apache-2.0":   {"Apache 2.0 License", "Apache 2.0 License"},
	"BSD-3-Clause": {"BSD 3-Clause License", "The BSD 3-Clause License"},
	"BSD-2-Clause": {"BSD 2-Clause License", "The BSD 2-Clause License"},
	"0BSD":         {"Zero-Clause BSD License", "Zero-Clause BSD License"},
	"ISC":          {"ISC License", "ISC License"},
	"MPL-2.0":      {"Mozilla Public License 2.0", "Mozilla Public License 2.0"},
}

// licenseFamilies lists the distinct licence families a module is under.
func licenseFamilies(m module) []string {
	var out []string
	seen := map[string]bool{}
	for _, lic := range m.licenses {
		if !seen[lic.family] {
			seen[lic.family] = true
			out = append(out, lic.family)
		}
	}
	return out
}

func familyHeading(fam string) string { return familyNames[fam].heading }
func familyField(fam string) string   { return familyNames[fam].field }

func indent(s, pad string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if strings.TrimSpace(l) == "" {
			lines[i] = ""
			continue
		}
		lines[i] = pad + l
	}
	return strings.Join(lines, "\n")
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "gennotices: %v\n", err)
	os.Exit(1)
}
