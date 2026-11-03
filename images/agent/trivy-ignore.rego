# Trivy ignore policy for the agent image (TRIVY_IGNORE_POLICY).
package trivy

import rego.v1

default ignore := false

# linux-libc-dev is the kernel's userspace headers, built from Ubuntu's
# kernel source package, so trackers mark it with every kernel CVE. No code
# of it runs, and kiki boots its own kernel.
ignore if input.PkgName == "linux-libc-dev"
