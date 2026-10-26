#!/usr/bin/env bash
# Run in a clean openRuyi build environment with installed RPM macros, not on
# the development host with source macros loaded by hand. No packages are
# installed by this script, and no dependency checks are disabled.
set -euo pipefail
export LC_ALL=C GOWORK=off GOTOOLCHAIN=local

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
blocked() { printf 'BLOCKED: %s\n' "$*" >&2; exit 2; }

if [[ $# != 2 ]]; then
    printf 'Usage: bash tests/native-combine.sh FIXTURE_DIRECTORY NEW_BUILD_DIRECTORY\n' >&2
    exit 2
fi

[[ ! -e "$2" && ! -L "$2" ]] || fail 'build directory already exists; refusing to replace it'
fixture=$(realpath -e -- "$1")
work=$(realpath -m -- "$2")
package=go-go2spec-combine-fixture
import_root=example.test/go2spec/fixture
spec="$fixture/$package/$package.spec"
[[ -f "$spec" && -f "$fixture/expected-provides.txt" && -f "$fixture/expected-tests.txt" ]] || fail 'incomplete fixture; generate it with TestAggregateNativeFixture first'
[[ ! -e "$work" && ! -L "$work" ]] || fail 'build directory already exists; refusing to replace it'
[[ "$work" =~ ^/[A-Za-z0-9_./-]+$ ]] || fail 'build directory must have a simple absolute path suitable for RPM macros'

# Check the OS before touching rpmdb. In particular, this is not an alternate
# way to query a development host whose native RPM database is unavailable.
[[ -r /etc/os-release ]] || blocked 'no operating-system identity available'
grep -Eqi '^ID="?openruyi"?$' /etc/os-release || blocked 'requires a real openRuyi build environment; this host is not openRuyi'
for tool in rpm rpmbuild rpm2cpio cpio go unzip sha256sum cmp diff sort; do
    command -v "$tool" >/dev/null || blocked "missing native build tool: $tool"
done
macro_version=$(rpm -q --qf '%{VERSION}' go-rpm-macros) || blocked 'cannot identify the installed go-rpm-macros RPM'
[[ "$macro_version" =~ ^[A-Za-z0-9._+~^]+$ ]] || blocked 'unsupported RPM version text'
# Only the checked literal version is inserted into this constant Lua expression;
# no spec text, RPM macros from a source tree, or arbitrary Lua is evaluated.
comparison=$(rpm --eval "%{lua:print(rpm.vercmp('$macro_version', '0.3'))}") || blocked 'native RPM version comparison failed'
case "$comparison" in
    0|1) ;;
    -1) blocked 'installed macros do not satisfy go-rpm-macros >= 0.3; prerelease 0.3~rc2 is not 0.3' ;;
    *) blocked 'cannot compare the installed macro version with native RPM' ;;
esac
rpm -V go-rpm-macros || blocked 'installed macro files differ from their RPM; use a clean build environment'

# A single successful mkdir reserves the directory. Never delete an old build
# tree, and retain all logs and artifacts on a failed build or assertion.
mkdir -- "$work"
mkdir -- "$work"/{SOURCES,SPECS,BUILD,BUILDROOT,RPMS,SRPMS}
cp -- "$spec" "$work/SPECS/"
cp -- "$fixture/$package/"*.zip "$work/SOURCES/"
cp -- "$fixture/expected-provides.txt" "$fixture/expected-tests.txt" "$work/"
rpm -q --qf '%{NAME} %{EPOCHNUM}:%{VERSION}-%{RELEASE}.%{ARCH} source=%{SOURCERPM}\n' go-rpm-macros > "$work/macros-rpm.txt"
rpm -qf -- "$(command -v rpmbuild)" "$(command -v rpm2cpio)" "$(command -v go)" > "$work/tool-owners.txt"
GO111MODULE=off go version > "$work/go-version.txt"
source_root=$(rpm --eval '%{go_sys_gopath}')
license_root=$(rpm --eval '%{_licensedir}')
[[ "$source_root" =~ ^/[A-Za-z0-9_./-]+$ && "$license_root" =~ ^/[A-Za-z0-9_./-]+$ ]] || fail 'expected installed Go and license path macros are unavailable'
printf '%s\n' "$source_root" > "$work/go-source-root.txt"
sha256sum "$work/SPECS/$package.spec" "$work/SOURCES/"*.zip "$work/expected-provides.txt" "$work/expected-tests.txt" > "$work/inputs.sha256"

# Do not use --nodeps, --nocheck, an alternate rpmdb, source --load macros, or
# definitions that replace the generated Version/Release/BuildRequires.
if ! rpmbuild -ba --define "_topdir $work" "$work/SPECS/$package.spec" > "$work/build.log" 2>&1; then
    fail "rpmbuild failed; see $work/build.log"
fi
shopt -s nullglob
rpms=("$work"/RPMS/*/*.rpm)
[[ ${#rpms[@]} == 1 ]] || fail 'expected exactly one binary source-library RPM'
rpm_file=${rpms[0]}
[[ $(rpm -qp --qf '%{NAME}' "$rpm_file") == "$package" ]] || fail 'unexpected binary RPM owner'
[[ $(rpm -qp --qf '%{VERSION}' "$rpm_file") == 99.0 ]] || fail 'aggregate RPM version was changed'
[[ $(rpm -qp --qf '%{ARCH}' "$rpm_file") == noarch ]] || fail 'source-library RPM is not noarch'

rpm -qp --provides "$rpm_file" > "$work/provides-all.txt"
grep '^go(' "$work/provides-all.txt" | sort > "$work/provides-go.txt"
sort "$work/expected-provides.txt" > "$work/provides-expected.txt"
diff -u "$work/provides-expected.txt" "$work/provides-go.txt"
rpm -qpl "$rpm_file" > "$work/payload-files.txt"
grep '/\.goipath$' "$work/payload-files.txt" | sort > "$work/markers-actual.txt"
: > "$work/markers-expected.txt"

index=0
while read -r capability operator version extra; do
    [[ "$capability" =~ ^go\([A-Za-z0-9_./-]+\)$ && "$operator" == = && "$version" =~ ^[0-9.]+$ && -z "$extra" ]] || fail 'invalid expected Provides row'
    import_path=${capability#go(}
    import_path=${import_path%)}
    marker="$source_root/$import_path/.goipath"
    printf '%s\n' "$marker" >> "$work/markers-expected.txt"
    rpm2cpio "$rpm_file" | cpio -i --to-stdout --quiet ".$marker" > "$work/marker-$index.actual"
    printf 'version:%s\n' "$version" > "$work/marker-$index.expected"
    cmp "$work/marker-$index.expected" "$work/marker-$index.actual"
    index=$((index + 1))
done < "$work/provides-expected.txt"
[[ $index == 4 ]] || fail 'expected four distinct library capabilities in this fixture'
sort -o "$work/markers-expected.txt" "$work/markers-expected.txt"
diff -u "$work/markers-expected.txt" "$work/markers-actual.txt"

# Compare the complete installed source tree, not just markers. This also
# catches a default BuildSystem install copying a second, unwanted source tree.
rpm -qp --qf '[%{FILENAMES}\t%{FILEMODES:perms}\n]' "$rpm_file" > "$work/payload-details.txt"
: > "$work/source-files-actual.txt"
while IFS=$'\t' read -r filename mode; do
    if [[ "$filename" == "$source_root/$import_root/"* && "$mode" != d* ]]; then
        [[ "$mode" == -* ]] || fail "non-regular source payload entry: $filename"
        printf '%s\n' "$filename" >> "$work/source-files-actual.txt"
    fi
done < "$work/payload-details.txt"
cp -- "$work/markers-expected.txt" "$work/source-files-expected.txt"
source_count=0
for index in 0 1; do
    if [[ $index == 0 ]]; then
        module_path=$import_root
        module_version=v1.2.3
    else
        module_path="$import_root/sub"
        module_version=v0.4.0
    fi
    archive="$work/SOURCES/module-$index-$module_version.zip"
    prefix="$module_path@$module_version/"
    unzip -Z -1 "$archive" > "$work/module-$index-members.txt"
    while IFS= read -r member; do
        [[ "$member" == "$prefix"* ]] || fail "unexpected ZIP root: $member"
        [[ "$member" != */ ]] || continue
        filename="$source_root/$module_path/${member#"$prefix"}"
        printf '%s\n' "$filename" >> "$work/source-files-expected.txt"
        unzip -p "$archive" "$member" > "$work/source-$source_count.expected"
        rpm2cpio "$rpm_file" | cpio -i --to-stdout --quiet ".$filename" > "$work/source-$source_count.actual"
        cmp "$work/source-$source_count.expected" "$work/source-$source_count.actual"
        source_count=$((source_count + 1))
    done < "$work/module-$index-members.txt"

    # Both license copies must survive RPM's %license handling independently.
    mapfile -t licenses < <(grep -F "$license_root/" "$work/payload-files.txt" | grep "/module-$index/LICENSE$")
    [[ ${#licenses[@]} == 1 ]] || fail "missing/ambiguous license copy for module-$index"
    rpm2cpio "$rpm_file" | cpio -i --to-stdout --quiet ".${licenses[0]}" > "$work/license-$index.actual"
    unzip -p "$archive" "${prefix}LICENSE" > "$work/license-$index.expected"
    cmp "$work/license-$index.expected" "$work/license-$index.actual"
done
[[ $source_count -gt 0 ]] || fail 'empty source inventory'
sort -o "$work/source-files-expected.txt" "$work/source-files-expected.txt"
sort -o "$work/source-files-actual.txt" "$work/source-files-actual.txt"
diff -u "$work/source-files-expected.txt" "$work/source-files-actual.txt"

count=0
while read -r test_name; do
    [[ "$test_name" =~ ^Test[A-Za-z0-9]+$ ]] || fail 'invalid expected test name'
    grep -F -- "--- PASS: $test_name (" "$work/build.log" >/dev/null || fail "native build did not report passing test $test_name"
    count=$((count + 1))
done < "$work/expected-tests.txt"
[[ $count == 4 ]] || fail 'expected four fixture tests'
sha256sum -c "$work/inputs.sha256" > "$work/inputs-unchanged.txt"
sha256sum "$rpm_file" > "$work/binary-rpm.sha256"
printf 'PASS: native build, four package versions, payload markers, licenses and tests verified.\nArtifacts: %s\n' "$work"
