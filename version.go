package main

import (
	"fmt"
	"io"
	"runtime/debug"
	"time"
)

const unknownVersion = "unknown"

func runtimeVersion() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return unknownVersion
	}
	return versionFromBuildSettings(info.Settings)
}

func versionFromBuildSettings(settings []debug.BuildSetting) string {
	var revision string
	var commitTime string
	for _, setting := range settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			commitTime = setting.Value
		}
	}
	if revision == "" || commitTime == "" {
		return unknownVersion
	}
	if len(revision) > 7 {
		revision = revision[:7]
	}
	if t, err := time.Parse(time.RFC3339, commitTime); err == nil {
		commitTime = t.Format("2006-01-02")
	}
	return fmt.Sprintf("%s %s", commitTime, revision)
}

func printVersion(w io.Writer) {
	fmt.Fprintf(w, "Version: %s\n", runtimeVersion())
}
