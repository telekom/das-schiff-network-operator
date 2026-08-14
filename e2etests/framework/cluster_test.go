package framework

import "testing"

func TestSplitYAMLDocuments(t *testing.T) {
	tests := []struct {
		name string
		data string
		want []string
	}{
		{
			name: "single document",
			data: "kind: A\n",
			want: []string{"kind: A"},
		},
		{
			name: "two documents",
			data: "kind: A\n---\nkind: B\n",
			want: []string{"kind: A", "kind: B"},
		},
		{
			// A header comment above the first separator splits off as its own
			// document. It is legal YAML, but it decodes to null and the object
			// decoder rejects the whole manifest with "Kind is missing in 'null'".
			name: "leading comment block",
			data: "# what this file is\n# and why\n---\nkind: A\n",
			want: []string{"kind: A"},
		},
		{
			name: "comment between documents",
			data: "kind: A\n---\n# just a note\n---\nkind: B\n",
			want: []string{"kind: A", "kind: B"},
		},
		{
			name: "trailing separator",
			data: "kind: A\n---\n",
			want: []string{"kind: A"},
		},
		{
			name: "comments only",
			data: "# nothing here\n",
			want: nil,
		},
		{
			// A '#' inside a value is not a comment, so the document stays. A
			// leading separator is left in place; the YAML decoder accepts it.
			name: "hash in a value",
			data: "---\nname: \"a # b\"\n",
			want: []string{"---\nname: \"a # b\""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitYAMLDocuments([]byte(tt.data))
			if len(got) != len(tt.want) {
				t.Fatalf("got %d documents %q, want %d %q", len(got), got, len(tt.want), tt.want)
			}
			for i := range got {
				if string(got[i]) != tt.want[i] {
					t.Errorf("document %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}
