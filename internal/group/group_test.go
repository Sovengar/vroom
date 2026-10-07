package group

import (
	"strings"
	"testing"

	"vroom/internal/manifest"
	"vroom/internal/scanner"
)

// primary = "" means no manifest at all; a secondary without a primary is allowed and then ignored.
func proj(name, primary, secondary string) scanner.Project {
	p := scanner.Project{
		Path: "/home/user/dev/" + name,
		Name: name,
	}
	if primary == "" && secondary == "" {
		return p // no manifest
	}
	p.Configured = true
	p.Manifest = &manifest.Manifest{Name: name, PrimaryGroup: primary, SecondaryGroup: secondary, Command: "run " + name}
	return p
}

func names(entries []Entry) string {
	var out []string
	for _, e := range entries {
		out = append(out, e.Project.Name)
	}
	return strings.Join(out, ",")
}

func TestArrangeSamePrimaryTogether(t *testing.T) {
	a := proj("api-java", "shop", "")
	b := proj("web-frontend", "shop", "")
	c := proj("api-go", "", "")

	entries := Arrange([]scanner.Project{c, a, b})
	if len(entries) != 3 {
		t.Fatalf("len = %d, want 3", len(entries))
	}

	if got := names(entries); got != "api-go,api-java,web-frontend" {
		t.Errorf("order = %q", got)
	}
	if entries[0].Primary != "" || entries[0].Secondary != "" {
		t.Errorf("api-go must have no groups, got %+v", entries[0])
	}
	if entries[1].Primary != "shop" || entries[2].Primary != "shop" {
		t.Errorf("shop members badly grouped: %+v %+v", entries[1], entries[2])
	}
	if entries[1].Secondary != "" || entries[2].Secondary != "" {
		t.Errorf("without secondary there must be no Secondary: %+v %+v", entries[1], entries[2])
	}

	if !IsPrimaryHeader(entries, 1) {
		t.Error("api-java must open the shop block")
	}
	if IsPrimaryHeader(entries, 2) {
		t.Error("web-frontend is not a header (same primary as the previous)")
	}
	if IsPrimaryHeader(entries, 0) {
		t.Error("api-go without primary must not be a header")
	}
	if IsSecondaryHeader(entries, 1) {
		t.Error("without secondary there must be no secondary header")
	}
}

func TestArrangeSingleMemberPrimary(t *testing.T) {
	a := proj("backend-vroom", "backend", "")
	entries := Arrange([]scanner.Project{a})
	if len(entries) != 1 || entries[0].Primary != "backend" || entries[0].Secondary != "" {
		t.Fatalf("got %+v", entries)
	}
	if !IsPrimaryHeader(entries, 0) {
		t.Error("single primary must show header")
	}
}

func TestArrangePrimaryInsertedAfterFirstSeen(t *testing.T) {
	x := proj("x", "", "")
	a := proj("a", "g1", "")
	y := proj("y", "", "")
	b := proj("b", "g1", "")

	entries := Arrange([]scanner.Project{x, a, y, b})
	if got := names(entries); got != "x,a,b,y" {
		t.Errorf("order = %q, want x,a,b,y", got)
	}
}

func TestArrangeInterleavedPrimariesSingleBlock(t *testing.T) {
	projects := []scanner.Project{
		proj("b1", "backend", ""), proj("b2", "backend", ""), proj("f1", "frontend", ""),
		proj("b3", "backend", ""), proj("f2", "frontend", ""), proj("solo", "", ""),
		proj("infra1", "infra", ""), proj("solo2", "", ""), proj("b4", "backend", ""),
		proj("b5", "backend", ""), proj("b6", "backend", ""), proj("f3", "frontend", ""),
		proj("f4", "frontend", ""), proj("f5", "frontend", ""),
	}

	entries := Arrange(projects)
	if len(entries) != len(projects) {
		t.Fatalf("len = %d, want %d", len(entries), len(projects))
	}

	seen := make(map[string]bool)
	for i, e := range entries {
		if !IsPrimaryHeader(entries, i) {
			continue
		}
		if seen[e.Primary] {
			t.Errorf("primary %q has more than one block (header at %d)", e.Primary, i)
		}
		seen[e.Primary] = true
	}
	for _, g := range []string{"backend", "frontend", "infra"} {
		if !seen[g] {
			t.Errorf("primary %q has no header", g)
		}
	}

	if got := names(entries); got != "b1,b2,b3,b4,b5,b6,f1,f2,f3,f4,f5,solo,infra1,solo2" {
		t.Errorf("order = %q", got)
	}
}

func TestArrangeNestedBlocksContiguous(t *testing.T) {
	a := proj("a", "vsocial", "backend")
	b := proj("b", "", "")
	c := proj("c", "vsocial", "infra")
	d := proj("d", "vsocial", "backend")

	entries := Arrange([]scanner.Project{a, b, c, d})
	if got := names(entries); got != "a,d,c,b" {
		t.Errorf("order = %q, want a,d,c,b", got)
	}
	if entries[0].Secondary != "backend" || entries[1].Secondary != "backend" || entries[2].Secondary != "infra" {
		t.Errorf("secondaries badly assigned: %+v %+v %+v", entries[0], entries[1], entries[2])
	}
	if entries[3].Primary != "" {
		t.Errorf("b must go inline without groups, got %+v", entries[3])
	}

	if !IsPrimaryHeader(entries, 0) || IsPrimaryHeader(entries, 1) || IsPrimaryHeader(entries, 2) || IsPrimaryHeader(entries, 3) {
		t.Error("only a must open the vsocial block")
	}
	if !IsSecondaryHeader(entries, 0) || IsSecondaryHeader(entries, 1) || !IsSecondaryHeader(entries, 2) || IsSecondaryHeader(entries, 3) {
		t.Errorf("secondary headers expected at 0 and 2: %v %v %v %v",
			IsSecondaryHeader(entries, 0), IsSecondaryHeader(entries, 1),
			IsSecondaryHeader(entries, 2), IsSecondaryHeader(entries, 3))
	}
}

func TestArrangePrimaryAtFirstMember(t *testing.T) {
	x := proj("x", "others", "")
	y := proj("y", "vsocial", "")
	z := proj("z", "others", "")

	entries := Arrange([]scanner.Project{x, y, z})
	if got := names(entries); got != "x,z,y" {
		t.Errorf("order = %q, want x,z,y", got)
	}
}

func TestArrangeMixedWithAndWithoutSecondary(t *testing.T) {
	b1 := proj("b1", "vsocial", "backend")
	f1 := proj("f1", "vsocial", "frontend")
	direct := proj("direct", "vsocial", "")

	entries := Arrange([]scanner.Project{b1, f1, direct})
	if got := names(entries); got != "b1,f1,direct" {
		t.Errorf("order = %q, want b1,f1,direct", got)
	}
	if entries[2].Primary != "vsocial" || entries[2].Secondary != "" {
		t.Errorf("direct must go under the primary without secondary, got %+v", entries[2])
	}
	if IsSecondaryHeader(entries, 2) {
		t.Error("the member without secondary does not open a secondary header")
	}
}

func TestArrangeSecondaryAtFirstMember(t *testing.T) {
	m1 := proj("m1", "vsocial", "b2")
	m2 := proj("m2", "vsocial", "b1")
	m3 := proj("m3", "vsocial", "b2")
	m4 := proj("m4", "vsocial", "")

	entries := Arrange([]scanner.Project{m1, m2, m3, m4})
	if got := names(entries); got != "m1,m3,m2,m4" {
		t.Errorf("order = %q, got %q", got, "m1,m3,m2,m4")
	}
}

func TestArrangeSecondaryWithoutPrimaryIgnored(t *testing.T) {
	p := proj("orphan", "", "infra")
	entries := Arrange([]scanner.Project{p})
	if len(entries) != 1 {
		t.Fatalf("len = %d, want 1", len(entries))
	}
	if entries[0].Primary != "" || entries[0].Secondary != "" {
		t.Errorf("secondary without primary must be discarded, got %+v", entries[0])
	}
	if IsSecondaryHeader(entries, 0) || IsPrimaryHeader(entries, 0) {
		t.Error("the inline row must not be a header of any level")
	}
}

func TestIsSecondaryHeaderAcrossPrimaries(t *testing.T) {
	a := proj("a", "vsocial", "backend")
	b := proj("b", "others", "backend")

	entries := Arrange([]scanner.Project{a, b})
	if !IsSecondaryHeader(entries, 1) {
		t.Error("changing primary must open a secondary header even if the name matches")
	}
	if !IsPrimaryHeader(entries, 1) {
		t.Error("b opens the block of primary others")
	}
}
