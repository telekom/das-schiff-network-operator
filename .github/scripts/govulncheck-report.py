# SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
#
# SPDX-License-Identifier: Apache-2.0

import json
import sys

# Split govulncheck findings into standard library and module findings.
#
# A finding whose first trace entry names a function is one that govulncheck
# considers reachable from this code. Only those decide the exit code.
#
# Module findings fail the job. Standard library findings only warn, because
# the fix is a Go toolchain upgrade, and the toolchain comes from setup-go,
# which can lag behind the newest patch release.

path = sys.argv[1]
decoder = json.JSONDecoder()
text = open(path).read()

findings = []
index = 0
while index < len(text):
    while index < len(text) and text[index] in " \n\r\t":
        index += 1
    if index >= len(text):
        break
    obj, index = decoder.raw_decode(text, index)
    if "finding" in obj:
        findings.append(obj["finding"])

called_std = {}
called_mod = {}
not_called = set()
for finding in findings:
    trace = finding["trace"][0]
    module = trace.get("module", "unknown")
    osv = finding["osv"]
    if not trace.get("function"):
        not_called.add((osv, module))
        continue
    target = called_std if module == "stdlib" else called_mod
    target[osv] = finding.get("fixed_version", "")

out = []
if called_mod:
    out.append("### govulncheck: module vulnerabilities found")
    out.append("")
    for osv in sorted(called_mod):
        out.append(f"- **{osv}** (fixed in {called_mod[osv] or 'unknown'})")
    out.append("")
if called_std:
    out.append("### govulncheck: standard library vulnerabilities (warning)")
    out.append("")
    out.append("These need a Go toolchain upgrade, not a dependency change.")
    out.append("")
    for osv in sorted(called_std):
        out.append(f"- {osv} (fixed in {called_std[osv] or 'unknown'})")
    out.append("")
if not_called:
    out.append(f"{len(not_called)} advisories affect required modules that this code does not call.")
    out.append("")
if not called_mod and not called_std:
    out.append("### govulncheck: no reachable vulnerabilities")
    out.append("")

print("\n".join(out))

if called_mod:
    print(f"govulncheck: {len(called_mod)} module vulnerability(ies) reachable from this code", file=sys.stderr)
    sys.exit(1)
if called_std:
    print(f"govulncheck: {len(called_std)} standard library vulnerability(ies); upgrade the Go toolchain", file=sys.stderr)
