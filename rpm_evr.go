package main

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type rpmEVRRequirement struct {
	Operator string `json:"operator"`
	EVR      rpmEVR `json:"evr"`
}

type rpmEVRComparator func(context.Context, rpmEVR, rpmEVR) (int, error)

type rpmCompareResult struct {
	comparison int
	err        error
}

func cachedRPMEVRComparator(compare rpmEVRComparator) rpmEVRComparator {
	cache := make(map[string]rpmCompareResult)
	return func(ctx context.Context, left, right rpmEVR) (int, error) {
		key := strings.Join([]string{
			normalizeEpoch(left.Epoch), left.Version, left.Release,
			normalizeEpoch(right.Epoch), right.Version, right.Release,
		}, "\x00")
		if cached, ok := cache[key]; ok {
			return cached.comparison, cached.err
		}
		comparison, err := compare(ctx, left, right)
		cache[key] = rpmCompareResult{comparison: comparison, err: err}
		return comparison, err
	}
}

func compareRPMEVR(ctx context.Context, left, right rpmEVR) (int, error) {
	if err := validateRPMEVR(left); err != nil {
		return 0, fmt.Errorf("invalid left RPM EVR: %w", err)
	}
	if err := validateRPMEVR(right); err != nil {
		return 0, fmt.Errorf("invalid right RPM EVR: %w", err)
	}
	leftEpoch, err := strconv.ParseUint(normalizeEpoch(left.Epoch), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid RPM epoch %q: %w", left.Epoch, err)
	}
	rightEpoch, err := strconv.ParseUint(normalizeEpoch(right.Epoch), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid RPM epoch %q: %w", right.Epoch, err)
	}
	if leftEpoch < rightEpoch {
		return -1, nil
	}
	if leftEpoch > rightEpoch {
		return 1, nil
	}
	if result, err := rpmVerCmp(ctx, left.Version, right.Version); err != nil || result != 0 {
		return result, err
	}
	if left.Release == "" && right.Release == "" {
		return 0, nil
	}
	if left.Release == "" || right.Release == "" {
		return 0, fmt.Errorf("RPM EVR release component is not comparable when only one side specifies it")
	}
	return rpmVerCmp(ctx, left.Release, right.Release)
}

func rpmVerCmp(ctx context.Context, left, right string) (int, error) {
	luaString := func(value string) string {
		parts := make([]string, len(value))
		for i := 0; i < len(value); i++ {
			parts[i] = strconv.Itoa(int(value[i]))
		}
		return "string.char(" + strings.Join(parts, ",") + ")"
	}
	expression := fmt.Sprintf(`%%{lua: print(rpm.vercmp(%s, %s))}`, luaString(left), luaString(right))
	output, err := exec.CommandContext(ctx, "rpm", "--eval", expression).Output()
	if err != nil {
		return 0, fmt.Errorf("RPM EVR comparison unavailable: %w", err)
	}
	result, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || result < -1 || result > 1 {
		return 0, fmt.Errorf("RPM EVR comparator returned invalid result %q", strings.TrimSpace(string(output)))
	}
	return result, nil
}

func rpmRequirementSatisfied(ctx context.Context, provider rpmProvider, requirement *rpmEVRRequirement, compare rpmEVRComparator) (bool, bool, error) {
	if requirement == nil {
		return true, true, nil
	}
	if provider.ProvideEVR == nil {
		return false, false, nil
	}
	cmp, err := compare(ctx, *provider.ProvideEVR, requirement.EVR)
	if err != nil {
		return false, false, err
	}
	switch strings.ToUpper(provider.Flags) {
	case "EQ":
		return compareRPMOperator(cmp, requirement.Operator), true, nil
	case "":
		return false, false, nil
	}
	return false, false, nil
}

func compareRPMOperator(cmp int, operator string) bool {
	switch operator {
	case "=":
		return cmp == 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	default:
		return false
	}
}
