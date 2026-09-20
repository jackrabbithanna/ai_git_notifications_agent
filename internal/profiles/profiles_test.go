package profiles

import (
	"strings"
	"testing"
)

func TestBuiltinsParse(t *testing.T) {
	ps, err := Builtins()
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]Profile{}
	for _, p := range ps {
		ids[p.ID] = p
	}
	civi, ok := ids["civicrm"]
	if !ok || ids[GenericID].ID == "" {
		t.Fatalf("builtins: %v", ids)
	}
	if !civi.MatchesRepo("github", "github.com", "civicrm/civicrm-core") || civi.MatchesRepo("github", "github.com", "civicrm/other") || civi.MatchesRepo("gitlab", "lab.civicrm.org", "dev/core") {
		t.Fatal("repo matching")
	}
	if l, ok := civi.LayerFor("Civi/Api4/Contact.php"); !ok || l.ID != "data_api" {
		t.Fatalf("layer: %+v %v", l, ok)
	}
	if l, ok := civi.LayerFor("CRM/Contact/Form/Edit.php"); !ok || l.ID != "ui" {
		t.Fatalf("layer ui: %+v", l)
	}
	if !civi.Ignored("tests/phpunit/api/v4/Foo.php") || civi.Ignored("CRM/Core/DAO.php") {
		t.Fatal("ignore")
	}
}

func TestParseValidation(t *testing.T) {
	if _, err := Parse([]byte("name: x\nlayers: []\n")); err == nil {
		t.Fatal("missing id must fail")
	}
	if _, err := Parse([]byte("id: x\nlayers:\n  - id: a\n    paths: ['[']\n")); err == nil {
		t.Fatal("bad glob must fail")
	}
	if _, err := Parse([]byte("id: x\nlayers:\n  - id: a\n    paths: ['**']\nsurface_patterns:\n  - id: p\n    pattern: '('\n")); err == nil {
		t.Fatal("bad regex must fail")
	}
	p, err := Parse([]byte("id: gl\nrepos: ['gitlab:lab.example.org/dev/core', 'gitlab:group/proj', 'github:o/r']\nlayers:\n  - id: a\n    paths: ['**']\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !p.MatchesRepo("gitlab", "lab.example.org", "dev/core") || !p.MatchesRepo("gitlab", "gitlab.com", "group/proj") || !p.MatchesRepo("github", "github.com", "o/r") || p.MatchesRepo("gitlab", "other.host", "dev/core") {
		t.Fatal("forge-qualified repo matching")
	}
}

func TestAnalyze(t *testing.T) {
	ps, _ := Builtins()
	var civi Profile
	for _, p := range ps {
		if p.ID == "civicrm" {
			civi = p
		}
	}
	files := []FileChange{
		{Path: "Civi/Api4/Contact.php", Status: "modified", Additions: 12, Deletions: 3, Patch: "@@ -1,3 +1,4 @@\n-  public function getFields() {\n+  public function getFields(bool $checkPermissions = TRUE) {\n+  // note\n"},
		{Path: "CRM/Core/BAO/CustomField.php", Status: "modified", Additions: 40, Deletions: 2, Patch: "+  public static function create(&$params) {\n"},
		{Path: "templates/CRM/Contact/Form/Edit.tpl", Status: "modified", Additions: 1, Deletions: 1, Patch: "-<div>\n+<div class=\"x\">\n"},
		{Path: "tests/phpunit/api/v4/ContactTest.php", Status: "added", Additions: 100, Deletions: 0, Patch: "+public function testX() {}\n"},
		{Path: "README.md", Status: "modified", Additions: 1, Deletions: 0},
		{Path: "weird/unknown.txt", Status: "added", Additions: 1, Deletions: 0},
	}
	r := Analyze(civi, "APIv4 - Add checkPermissions to getFields (BREAKING for callers)", "Removes the old signature.", []string{"master"}, files)
	if r.TotalFiles != 6 || r.Ignored != 2 || len(r.Unclassified) != 1 {
		t.Fatalf("counts: %+v", r)
	}
	if len(r.Layers) < 2 || r.Layers[0].ID != "data_api" || r.Layers[0].Weight != 1.0 || len(r.Layers[0].Files) != 2 || r.Layers[0].Additions != 52 {
		t.Fatalf("layers: %+v", r.Layers)
	}
	if r.MaxWeight != 1.0 {
		t.Fatalf("max weight: %v", r.MaxWeight)
	}
	joined := strings.Join(r.Signals, ",")
	if !strings.Contains(joined, "BREAKING") || !strings.Contains(joined, "APIv4") || !strings.Contains(joined, "signature") || !strings.Contains(joined, "remove") {
		t.Fatalf("signals: %v", r.Signals)
	}
	var ids []string
	for _, h := range r.SurfaceHits {
		ids = append(ids, h.PatternID)
	}
	if len(r.SurfaceHits) < 2 || !strings.Contains(strings.Join(ids, ","), "php_public_sig") {
		t.Fatalf("surface hits: %+v", r.SurfaceHits)
	}
	for _, h := range r.SurfaceHits {
		if strings.HasPrefix(h.Path, "tests/") {
			t.Fatal("ignored files must not produce surface hits")
		}
	}
	if len(r.TopFiles) == 0 || r.TopFiles[0].Path != "CRM/Core/BAO/CustomField.php" {
		t.Fatalf("top files order (weight, then size): %+v", r.TopFiles)
	}
	big := FileChange{Path: "Civi/Api4/Big.php", Patch: strings.Repeat("+x\n", 5000)}
	r2 := Analyze(civi, "t", "", nil, []FileChange{big})
	if len(r2.TopFiles[0].Patch) > maxPatchPerFile+4 {
		t.Fatalf("patch not trimmed: %d", len(r2.TopFiles[0].Patch))
	}
}
