/*
 * Copyright 2026 Fajar Maulana Firdaus.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 * https://www.apache.org/licenses/LICENSE-2.0
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 *
 */

package platform

import (
	"fmt"
	"os"
	"slices"
	"strings"
)

const EnvVar = "TURBOLIFT_PLATFORM"

func IsGitLab() bool {
	return strings.EqualFold(os.Getenv(EnvVar), "gitlab")
}

func Validate() error {
	switch strings.ToLower(os.Getenv(EnvVar)) {
	case "", "github", "gitlab":
		return nil
	default:
		return fmt.Errorf("unsupported %s value %q, use github or gitlab", EnvVar, os.Getenv(EnvVar))
	}
}

// SplitGitLabPath splits a repos.txt entry such as group/subgroup/repo. GitLab groups can nest, so everything
// between the host and the last part is the namespace. The first part counts as a host only when it contains
// a dot and at least two parts follow it, so a top-level group with a dot in its name needs the host in front.
func SplitGitLabPath(fullRepoName string) (host string, namespace string, repo string, err error) {
	parts := strings.Split(fullRepoName, "/")
	if slices.Contains(parts, "") {
		return "", "", "", fmt.Errorf("unable to parse GitLab repository %q", fullRepoName)
	}

	if len(parts) >= 3 && strings.Contains(parts[0], ".") {
		host, parts = parts[0], parts[1:]
	}

	if len(parts) < 2 {
		return "", "", "", fmt.Errorf("unable to parse GitLab repository %q", fullRepoName)
	}

	return host, strings.Join(parts[:len(parts)-1], "/"), parts[len(parts)-1], nil
}
