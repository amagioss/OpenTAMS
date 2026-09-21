// Command gennotices regenerates the THIRD-PARTY-NOTICES directory from the
// Go module cache.
//
// Scope is every module linked into the released artefacts — the modules
// reachable from ./cmd/..., which is what ends up inside the opentams and
// tamsctl binaries and the published container images. Build- and test-only
// modules are excluded because they are never redistributed.
//
// For each module the tool reads the licence file shipped in the module
// cache, classifies it, and pulls out the copyright notices. Modules are then
// grouped by licence family, one output file each.
//
// Within a family the licence text is printed once, after the component list,
// rather than repeated under every module. That is the standard attribution
// layout and it still satisfies the reproduce-the-notice conditions: each
// module's own copyright line is listed, and the shared terms follow. A module
// whose licence text is NOT the canonical template for its family is the
// exception — its text is reproduced verbatim in full, so a locally modified
// or unusual licence can never be silently collapsed into the standard one.
//
//	go run ./tools/gennotices          # rewrite THIRD-PARTY-NOTICES/
//	go run ./tools/gennotices -check   # fail if the committed files are stale
package main

import (
	"bytes"
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
	outDir      = "THIRD-PARTY-NOTICES"
	ownModule   = "github.com/amagioss/opentams"
	copyrightCO = "Copyright © 2026 Amagi Media Labs Limited"
)

// familyOrder fixes the order the licence families appear in, both in the
// index table and as files on disk. Alphabetical would put 0BSD first, which
// buries the two families that carry almost every module.
var familyOrder = []string{"MIT", "Apache-2.0", "BSD-3-Clause", "BSD-2-Clause", "0BSD", "ISC", "MPL-2.0"}

// licenseDoc is one licence a module is distributed under. A module usually
// has exactly one; a dual-licensed module has several, either as separate
// files or as several licences inside a single file.
type licenseDoc struct {
	file      string
	family    string
	text      string
	canonical bool
	combined  bool
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
	check := flag.Bool("check", false, "verify the committed files are up to date instead of writing them")
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

	for i := range mods {
		if err := mods[i].classify(); err != nil {
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

	files := map[string]string{
		"README.md": renderIndex(byFamily, len(mods)),
	}
	for fam, list := range byFamily {
		sort.Slice(list, func(i, j int) bool { return list[i].mod.Path < list[j].mod.Path })
		files[fam+".txt"] = renderFamily(fam, list)
	}

	dir := filepath.Join(root, outDir)
	if *check {
		if err := verify(dir, files); err != nil {
			fatal(err)
		}
		fmt.Printf("THIRD-PARTY-NOTICES is up to date (%d modules, %d families)\n", len(mods), len(byFamily))
		return
	}
	if err := write(dir, files); err != nil {
		fatal(err)
	}
	fmt.Printf("wrote %s: %d modules across %d licence families\n", outDir, len(mods), len(byFamily))
}

func repoRoot() (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}").Output()
	if err != nil {
		return "", fmt.Errorf("locating module root: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// linkedModules returns the third-party modules reachable from ./cmd/...,
// deduplicated and sorted. Packages from the standard library carry no module
// and are skipped, as is our own module.
func linkedModules(root string) ([]module, error) {
	cmd := exec.Command("go", "list", "-deps", "-json", "./cmd/...")
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("go list -deps: %w: %s", err, stderr.String())
	}

	type pkg struct {
		Module *struct {
			Path    string
			Version string
			Dir     string
		}
	}

	seen := map[string]module{}
	dec := json.NewDecoder(bytes.NewReader(out))
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			return nil, fmt.Errorf("decoding go list output: %w", err)
		}
		if p.Module == nil || p.Module.Dir == "" {
			continue
		}
		if p.Module.Path == ownModule || strings.HasPrefix(p.Module.Path, ownModule+"/") {
			continue
		}
		seen[p.Module.Path] = module{Path: p.Module.Path, Version: p.Module.Version, Dir: p.Module.Dir}
	}

	mods := make([]module, 0, len(seen))
	for _, m := range seen {
		mods = append(mods, m)
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].Path < mods[j].Path })
	return mods, nil
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

func (m *module) classify() error {
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
				m.licenses = append(m.licenses, licenseDoc{
					file:     name,
					family:   fam,
					text:     text,
					combined: len(fams) > 1,
					// A file covering several licences can never be replaced
					// by one family's shared text, so it is always reproduced.
					canonical: len(fams) == 1 && matchesCanonical(fam, text),
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

// grantAnchor is the first phrase of the operative grant in each family's
// text. Everything before it — the licence title, the copyright line, the
// occasional preamble — varies between projects without changing the terms,
// so the comparison starts at the anchor and ignores the lead-in.
var grantAnchor = map[string]string{
	"MIT":          "permission is hereby granted, free of charge",
	"BSD-3-Clause": "redistribution and use in source and binary forms",
	"BSD-2-Clause": "redistribution and use in source and binary forms",
	"0BSD":         "permission to use, copy, modify",
	"ISC":          "permission to use, copy, modify",
}

// canonicalBody reduces a licence text to its operative terms, normalized for
// comparison. A module whose body matches the family template is covered by
// the shared terms printed once per family; anything else gets reproduced in
// full under its own entry.
func canonicalBody(fam, text string) string {
	n := normalize(text)
	if anchor, ok := grantAnchor[fam]; ok {
		if i := strings.Index(n, anchor); i >= 0 {
			n = n[i:]
		}
	}
	return strings.TrimSpace(n)
}

func matchesCanonical(fam, text string) bool {
	tmpl, ok := licenseTemplates[fam]
	if !ok {
		// Apache-2.0 is verified by family detection alone: the text is long,
		// and every module ships the same upstream file.
		return fam == "Apache-2.0"
	}
	return canonicalBody(fam, text) == canonicalBody(fam, tmpl)
}

func renderIndex(byFamily map[string][]entry, total int) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Third-Party Software Licenses\n\n")
	fmt.Fprintf(&b, "OpenTAMS (%s) is licensed under the Apache License 2.0.\nThe full text is in the [`LICENSE`](../LICENSE) file at the root of this\nrepository.\n\n", copyrightCO)
	fmt.Fprintf(&b, "OpenTAMS uses third-party open-source software components. Each component\nremains subject to its respective copyright and license terms.\n\n")
	fmt.Fprintf(&b, "The files in this directory identify the applicable third-party components,\nversions, licenses, copyright notices, and license conditions.\n\n")

	fmt.Fprintf(&b, "| License | Components | Notices |\n|---|---|---|\n")
	for _, fam := range familyOrder {
		list, ok := byFamily[fam]
		if !ok {
			continue
		}
		fmt.Fprintf(&b, "| %s | %d | [`%s.txt`](%s.txt) |\n", fam, len(list), fam, fam)
	}
	fmt.Fprintf(&b, "\nA dual-licensed module is listed under every license it is distributed\nunder, so these counts add up to more than the module total below.\n\n")

	fmt.Fprintf(&b, "## Scope\n\n")
	fmt.Fprintf(&b, "These are the %d modules linked into the released `opentams` and `tamsctl`\nbinaries and the published container images — every module reachable from\n`./cmd/...`. Go links statically, so all of them are redistributed inside\nthose artefacts.\n\n", total)
	fmt.Fprintf(&b, "Modules used only to build or test OpenTAMS are not listed. They are never\nredistributed, so no attribution obligation attaches to them.\n\n")

	fmt.Fprintf(&b, "## Layout\n\n")
	fmt.Fprintf(&b, "Each file lists its components first, with the module path, version and\ncopyright notice, and then gives the license terms once. A component whose\nlicense text differs from the standard text for its family is reproduced in\nfull under its own entry instead.\n\n")

	fmt.Fprintf(&b, "## Regenerating\n\n")
	fmt.Fprintf(&b, "These files are generated from the Go module cache. Do not edit them by hand.\n\n")
	fmt.Fprintf(&b, "```bash\nmake notices        # rewrite this directory\nmake notices-check  # fail if it is stale\n```\n\n")
	fmt.Fprintf(&b, "Regenerate after any dependency change. CI runs `make notices-check`.\n")
	return b.String()
}

func renderFamily(fam string, entries []entry) string {
	var b strings.Builder
	rule := strings.Repeat("=", 78)
	sub := strings.Repeat("-", 78)

	fmt.Fprintf(&b, "%s\n%s Components\n%s\n\n", rule, familyHeading(fam), rule)
	fmt.Fprintf(&b, "OpenTAMS uses the third-party components listed below under the %s.\n", familyProse(fam))
	fmt.Fprintf(&b, "Each component remains subject to its own copyright notice, reproduced with\nthe component. The license terms follow the component list and apply to every\ncomponent listed, except where an entry reproduces its own text.\n\n")

	for _, e := range entries {
		m, lic := e.mod, e.lic
		fmt.Fprintf(&b, "%s\n", sub)
		fmt.Fprintf(&b, "Module:    %s\n", m.Path)
		fmt.Fprintf(&b, "Version:   %s\n", m.Version)
		fmt.Fprintf(&b, "License:   %s\n", familyField(fam))
		fmt.Fprintf(&b, "SPDX-License-Identifier: %s\n", fam)
		if len(m.copyrights) == 0 {
			fmt.Fprintf(&b, "Copyright: Not stated separately by the module; see the module source.\n")
		}
		for i, c := range m.copyrights {
			label := "Copyright:"
			if i > 0 {
				label = "          "
			}
			fmt.Fprintf(&b, "%s %s\n", label, c)
		}
		if m.inheritedFrom != "" {
			fmt.Fprintf(&b, "           (this module ships no notice of its own; the notice above is\n            that of %s, the project that publishes it)\n", m.inheritedFrom)
		}
		if len(m.licenses) > 1 {
			fmt.Fprintf(&b, "\nThis module is dual-licensed under %s, and is listed under\neach. This entry covers its %s file.\n",
				strings.Join(licenseFamilies(m), " and "), lic.file)
		}
		if fam == "Apache-2.0" && m.noticeText != "" {
			fmt.Fprintf(&b, "\nAttribution notices, reproduced from this module's NOTICE file as required\nby Apache License 2.0 section 4(d):\n\n")
			fmt.Fprintf(&b, "%s\n", indent(m.noticeText, "    "))
		}
		switch {
		case lic.combined:
			fmt.Fprintf(&b, "\nThis component's %s file covers more than one license and is reproduced\nhere in full:\n\n", lic.file)
			fmt.Fprintf(&b, "%s\n", indent(strings.TrimRight(lic.text, "\n"), "    "))
		case !lic.canonical:
			fmt.Fprintf(&b, "\nThis component's license text differs from the standard text below and is\nreproduced here in full:\n\n")
			fmt.Fprintf(&b, "%s\n", indent(strings.TrimRight(lic.text, "\n"), "    "))
		}
		fmt.Fprintf(&b, "\n")
	}

	fmt.Fprintf(&b, "%s\nLicense Terms — %s\n%s\n\n", rule, familyField(fam), rule)
	if fam == "Apache-2.0" {
		fmt.Fprintf(&b, "Licensed under the Apache License, Version 2.0. The full license text is in\nthe LICENSE file at the root of this repository, and at\nhttp://www.apache.org/licenses/LICENSE-2.0\n\n")
		fmt.Fprintf(&b, "The attribution notices required by section 4(d) are reproduced with each\ncomponent above.\n")
		return b.String()
	}
	fmt.Fprintf(&b, "%s\n", strings.TrimRight(licenseTemplates[fam], "\n"))
	return b.String()
}

// familyNames carries the three ways each licence has to be named: the
// section heading, the value of the per-component "License:" field, and the
// form that reads correctly inside a sentence.
var familyNames = map[string]struct{ heading, field, prose string }{
	"MIT":          {"MIT License", "The MIT License", "MIT License"},
	"Apache-2.0":   {"Apache 2.0 License", "Apache 2.0 License", "Apache License, Version 2.0"},
	"BSD-3-Clause": {"BSD 3-Clause License", "The BSD 3-Clause License", "BSD 3-Clause License"},
	"BSD-2-Clause": {"BSD 2-Clause License", "The BSD 2-Clause License", "BSD 2-Clause License"},
	"0BSD":         {"Zero-Clause BSD License", "Zero-Clause BSD License", "Zero-Clause BSD License"},
	"ISC":          {"ISC License", "ISC License", "ISC License"},
	"MPL-2.0":      {"Mozilla Public License 2.0", "Mozilla Public License 2.0", "Mozilla Public License 2.0"},
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
func familyProse(fam string) string   { return familyNames[fam].prose }

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

func write(dir string, files map[string]string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	existing, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range existing {
		if _, keep := files[e.Name()]; !keep && !e.IsDir() {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(dir, n), []byte(files[n]), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func verify(dir string, files map[string]string) error {
	var stale []string
	for name, want := range files {
		got, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			stale = append(stale, name+" (missing)")
			continue
		}
		if string(got) != want {
			stale = append(stale, name)
		}
	}
	if len(stale) > 0 {
		sort.Strings(stale)
		return fmt.Errorf("THIRD-PARTY-NOTICES is stale, run `make notices`:\n  %s",
			strings.Join(stale, "\n  "))
	}
	return nil
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "gennotices: %v\n", err)
	os.Exit(1)
}
