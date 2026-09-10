package gog

import (
	"encoding/json"
	"testing"
)

func TestExtractCode(t *testing.T) {
	cases := map[string]string{
		"abc123":     "abc123",
		"  abc123\n": "abc123",
		"https://embed.gog.com/on_login_success?origin=client&code=XYZ789": "XYZ789",
		"https://embed.gog.com/on_login_success?code=XYZ789&origin=client": "XYZ789",
		"code=plain&x=1": "plain",
	}
	for in, want := range cases {
		if got := ExtractCode(in); got != want {
			t.Errorf("ExtractCode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProductDecodingTolerance(t *testing.T) {
	raw := `{
	  "id": 1207658924, "title": "The Witcher", "slug": "the_witcher",
	  "downloads": {
	    "installers": [
	      {"id": "installer_windows_en", "name": "The Witcher", "os": "windows", "language": "en", "language_full": "English",
	       "version": null, "total_size": "1234", "files": [{"id": "en1installer0", "size": 1234, "downlink": "https://api.gog.com/products/1/downlink/installer/en1installer0"}]}
	    ],
	    "bonus_content": [
	      {"id": 55, "name": "manual", "type": "manuals", "count": 1, "total_size": 10.0,
	       "files": [{"id": 9876, "size": "10", "downlink": "https://api.gog.com/products/1/downlink/manuals/9876"}]}
	    ]
	  },
	  "expanded_dlcs": [{"id": 2, "title": "DLC", "downloads": {"installers": [], "bonus_content": []}}]
	}`
	var p Product
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		t.Fatal(err)
	}
	inst := p.Downloads.Installers[0]
	if inst.Version != "" || inst.TotalSize != 1234 || inst.Files[0].Size != 1234 || string(inst.Files[0].ID) != "en1installer0" {
		t.Errorf("unexpected installer: %+v", inst)
	}
	b := p.Downloads.BonusContent[0]
	if string(b.ID) != "55" || b.TotalSize != 10 || string(b.Files[0].ID) != "9876" || b.Files[0].Size != 10 {
		t.Errorf("unexpected bonus: %+v", b)
	}
	if len(p.ExpandedDLCs) != 1 || p.ExpandedDLCs[0].ID != 2 {
		t.Errorf("unexpected dlcs: %+v", p.ExpandedDLCs)
	}
}

func TestParseChecksum(t *testing.T) {
	xml := `<file name="setup_the_witcher_1.5.exe" available="1" notavailable="0" md5="9e107d9d372bb6826bd81d3542a419d6" chunks="2" timestamp="2020-01-01 00:00:00" total_size="2000">
	<chunk id="0" from="0" to="999" method="md5">aaa</chunk><chunk id="1" from="1000" to="1999" method="md5">bbb</chunk></file>`
	cs, err := ParseChecksum([]byte(xml))
	if err != nil {
		t.Fatal(err)
	}
	if cs.Name != "setup_the_witcher_1.5.exe" || cs.MD5 != "9e107d9d372bb6826bd81d3542a419d6" || cs.TotalSize != 2000 {
		t.Errorf("unexpected checksum: %+v", cs)
	}
}

func TestNormalizeImageAndFilename(t *testing.T) {
	if got := NormalizeImage("//images-1.gog-statics.com/abc"); got != "https://images-1.gog-statics.com/abc_392.jpg" {
		t.Errorf("NormalizeImage = %q", got)
	}
	if got := NormalizeImage("https://x/y.png"); got != "https://x/y.png" {
		t.Errorf("NormalizeImage = %q", got)
	}
	if got := FilenameFromURL("https://cdn.gog.com/secure/offline/setup_the_witcher_1.5_%28a%29.exe?token=1"); got != "setup_the_witcher_1.5_(a).exe" {
		t.Errorf("FilenameFromURL = %q", got)
	}
}
