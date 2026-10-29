# SPDX-FileCopyrightText: (C) 2026 Institute of Software, Chinese Academy of Sciences (ISCAS)
# SPDX-FileCopyrightText: (C) 2026 openRuyi Project Contributors
#
# SPDX-License-Identifier: MulanPSL-2.0

%global go2spec_combine 1
%global go_import_path golang.org/x

%global go_submodules %{expand:
golang.org/x/sys 0.29.0
golang.org/x/term 0.28.0
}

Name:           go-golang-x-sys-term
Version:        20261029
Release:        %autorelease
Summary:        Go library sources for golang.org/x
License:        BSD-3-Clause
URL:            https://go.dev/x
# Module: golang.org/x/sys@v0.29.0
#!RemoteAsset:  sha256:1d759c017d09328a4dacdd4a1a0170cf21641995c6e3ca1f96213369d73c6846
Source0:        https://proxy.golang.org/golang.org/x/sys/@v/v0.29.0.zip#/module-0-v0.29.0.zip
# Module: golang.org/x/term@v0.28.0
#!RemoteAsset:  sha256:ede5a49eac0946d10051703fa19f9f05c4cac3a217e55b7313e24b6c15ade914
Source1:        https://proxy.golang.org/golang.org/x/term/@v/v0.28.0.zip#/module-1-v0.28.0.zip

BuildArch:      noarch
BuildSystem:    golangmodules

BuildRequires:  go >= 1.18
BuildRequires:  go-rpm-macros >= 0.3
BuildRequires:  coreutils
BuildRequires:  unzip

%description
Go library sources for the following independently versioned modules:
- golang.org/x/sys v0.29.0
- golang.org/x/term v0.28.0

%prep
%setup -q -c -T

unzip -q "%{SOURCE0}" -d module-0
mkdir -p "gopath/src/golang.org/x/sys"
cp -a "module-0/golang.org/x/sys@v0.29.0/." "gopath/src/golang.org/x/sys/"
install -D -p -m 0644 "module-0/golang.org/x/sys@v0.29.0/LICENSE" "licenses/module-0/LICENSE"

unzip -q "%{SOURCE1}" -d module-1
mkdir -p "gopath/src/golang.org/x/term"
cp -a "module-1/golang.org/x/term@v0.28.0/." "gopath/src/golang.org/x/term/"
install -D -p -m 0644 "module-1/golang.org/x/term@v0.28.0/LICENSE" "licenses/module-1/LICENSE"

%build

%install
export GO111MODULE=off GOWORK=off GOTOOLCHAIN=local GOFLAGS= GOEXPERIMENT=
export GOPROXY=off GOSUMDB=off
mkdir -p "%{buildroot}%{go_sys_gopath}/%{go_import_path}"
cp -a "gopath/src/%{go_import_path}/." "%{buildroot}%{go_sys_gopath}/%{go_import_path}/"
%go_generate_provides -i %{go_import_path} -v %{version}

%check
export GO111MODULE=off GOWORK=off GOTOOLCHAIN=local GOFLAGS= GOEXPERIMENT=
export GOPROXY=off GOSUMDB=off
export GOPATH="$PWD/gopath:%{gopath}"
export GOCACHE="$PWD/.gocache"
%{__go} test %{go_test_flags_default} "%{go_import_path}/..."

%files
%license licenses/
%{go_sys_gopath}/%{go_import_path}

%changelog
%autochangelog
