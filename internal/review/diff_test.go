package review

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseUnifiedDiffTracksFilesHunksAndLineNumbers(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/internal/config.go b/internal/config.go
index 1111111..2222222 100644
--- a/internal/config.go
+++ b/internal/config.go
@@ -10,3 +10,4 @@ func load() {
 	name := "talon"
-	timeout := 10
+	timeout := 30
+	password := "not-for-production"
 	return
diff --git a/internal/new.go b/internal/new.go
new file mode 100644
--- /dev/null
+++ b/internal/new.go
@@ -0,0 +1,2 @@
+package internal
+const enabled = true
\ No newline at end of file
`

	files, err := ParseUnifiedDiff(diff)
	require.NoError(t, err)
	require.Len(t, files, 2)

	modified := files[0]
	require.Equal(t, "internal/config.go", modified.OldPath)
	require.Equal(t, "internal/config.go", modified.NewPath)
	require.Equal(t, FileModified, modified.Status)
	require.Len(t, modified.Hunks, 1)
	require.Equal(t, 10, modified.Hunks[0].OldStart)
	require.Equal(t, 10, modified.Hunks[0].NewStart)
	require.Equal(t, ChangedLine{
		Kind: LineAdded, NewLine: 12, Content: `	password := "not-for-production"`,
	}, modified.Hunks[0].Lines[3])

	added := files[1]
	require.Empty(t, added.OldPath)
	require.Equal(t, "internal/new.go", added.NewPath)
	require.Equal(t, FileAdded, added.Status)
	require.Equal(t, 1, added.Hunks[0].Lines[0].NewLine)
	require.Equal(t, 2, added.Hunks[0].Lines[1].NewLine)
}

func TestParseUnifiedDiffRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	_, err := ParseUnifiedDiff("not a unified diff")
	require.EqualError(t, err, "review: no file changes found in unified diff")

	_, err = ParseUnifiedDiff("--- a/a.go\n+++ b/a.go\n@@ invalid\n")
	require.ErrorContains(t, err, "malformed hunk header")
}

func TestParseUnifiedDiffSupportsDeletedFile(t *testing.T) {
	t.Parallel()

	diff := `diff --git a/legacy.go b/legacy.go
deleted file mode 100644
--- a/legacy.go
+++ /dev/null
@@ -1 +0,0 @@
-package legacy
`
	files, err := ParseUnifiedDiff(diff)
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, FileDeleted, files[0].Status)
	require.Equal(t, "legacy.go", files[0].Path())
}
