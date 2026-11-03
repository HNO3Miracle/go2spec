package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"

	"golang.org/x/mod/module"
)

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }

func (values *stringListFlag) Set(value string) error {
	if strings.TrimSpace(value) == "" {
		return fmt.Errorf("value must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func mainRepositoryQuery(args []string) int {
	flags := flag.NewFlagSet("repo-query", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var distro, repository, baseURL, arch, specPath, format string
	var keyrings, explicitRequirements, moduleRequirements stringListFlag
	var requireSignature bool
	flags.StringVar(&distro, "distro", "", "Target distribution identity")
	flags.StringVar(&repository, "repository", "", "Target repository identity or HTTPS repository base URL")
	flags.StringVar(&baseURL, "repository-url", "", "HTTPS repository base URL (optional when --repository is a URL)")
	flags.StringVar(&arch, "arch", "", "Target binary package architecture")
	flags.StringVar(&specPath, "spec", "", "Read go(...) dependencies from an RPM spec")
	flags.StringVar(&format, "format", "text", "Output format: text or json")
	flags.Var(&keyrings, "gpg-keyring", "Trusted GPG keyring; may be repeated")
	flags.BoolVar(&requireSignature, "require-signature", false, "Require and verify repomd.xml.asc with the supplied keyring")
	flags.Var(&explicitRequirements, "require", "Explicit RPM requirement, e.g. go(example.org/x)>=0:1.2-1; may be repeated")
	flags.Var(&moduleRequirements, "module-require", "Go Module provenance, module@canonical-version; may be repeated")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if format != "text" && format != "json" {
		log.Printf("repo-query: unsupported format %q", format)
		return 2
	}
	if strings.TrimSpace(arch) == "" {
		log.Print("repo-query: --arch must be specified")
		return 2
	}
	if specPath == "" && len(flags.Args()) == 0 && len(explicitRequirements) == 0 {
		log.Print("repo-query: provide --spec, capability arguments, or --require")
		return 2
	}
	if strings.Contains(repository, "://") {
		if baseURL != "" {
			log.Print("repo-query: use either URL-valued --repository or --repository-url, not both")
			return 2
		}
		baseURL = repository
		parsed, err := url.Parse(repository)
		if err != nil {
			log.Printf("repo-query: parse repository URL: %v", err)
			return 2
		}
		repository = strings.Trim(parsed.Host+parsed.Path, "/")
	}
	if baseURL == "" {
		log.Print("repo-query: --repository-url is required when --repository is an identity")
		return 2
	}
	dependencies, specErrors, err := queryDependenciesFromInputs(specPath, arch, flags.Args(), explicitRequirements, moduleRequirements)
	if err != nil {
		log.Printf("repo-query: %v", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	catalog, err := loadRepositoryCatalog(ctx, repositoryOptions{
		Distro: distro, Repository: repository, BaseURL: baseURL, Arch: arch,
		Keyrings: keyrings, RequireSignature: requireSignature,
	})
	if err != nil {
		log.Printf("repo-query: %v", err)
		return 1
	}
	report := diagnoseRepositoryDependencies(ctx, catalog, dependencies, cachedRPMEVRComparator(compareRPMEVR))
	report.Errors = append(report.Errors, specErrors...)
	slices.Sort(report.Errors)
	report.Errors = slices.Compact(report.Errors)
	if format == "json" {
		data, err := reportJSON(report)
		if err != nil {
			log.Printf("repo-query: encode report: %v", err)
			return 1
		}
		fmt.Println(string(data))
	} else {
		fmt.Print(renderRepositoryReport(report))
		for _, reportError := range report.Errors {
			fmt.Printf("\n[unknown] input: %s\n", reportError)
		}
	}
	if len(report.Errors) != 0 {
		return 1
	}
	return 0
}

func queryDependenciesFromInputs(specPath, arch string, capabilities, explicitRequirements, moduleRequirements []string) ([]repositoryDependency, []string, error) {
	var dependencies []repositoryDependency
	var errorsFound []string
	if specPath != "" {
		specDependencies, specErrors, err := readRepositorySpec(specPath, arch)
		if err != nil {
			return nil, nil, fmt.Errorf("read spec: %w", err)
		}
		dependencies = append(dependencies, specDependencies...)
		errorsFound = append(errorsFound, specErrors...)
	}
	for _, raw := range capabilities {
		capability, err := parseRepositoryCapability(raw)
		if err != nil {
			errorsFound = append(errorsFound, err.Error())
			continue
		}
		dependencies = append(dependencies, repositoryDependency{Capability: capability, Scope: "explicit"})
	}
	for _, raw := range explicitRequirements {
		dependency, err := parseRepositoryRPMRequirement(raw)
		if err != nil {
			errorsFound = append(errorsFound, err.Error())
			continue
		}
		dependencies = append(dependencies, dependency)
	}
	var modules []module.Version
	for _, raw := range moduleRequirements {
		moduleVersion, err := parseRepositoryModuleRequirement(raw)
		if err != nil {
			errorsFound = append(errorsFound, err.Error())
			continue
		}
		modules = append(modules, moduleVersion)
	}
	errorsFound = append(errorsFound, attachRepositoryModuleRequirements(dependencies, modules)...)
	slices.Sort(errorsFound)
	errorsFound = slices.Compact(errorsFound)
	return dependencies, errorsFound, nil
}
