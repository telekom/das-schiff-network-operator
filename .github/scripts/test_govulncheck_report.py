# SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
# SPDX-License-Identifier: Apache-2.0

"""Run with python3 .github/scripts/test_govulncheck_report.py."""
import json
from pathlib import Path
import subprocess
import sys
import tempfile

report = Path(__file__).with_name("govulncheck-report.py")
with tempfile.TemporaryDirectory() as directory:
    path = Path(directory) / "scan.json"
    for module, trace, expected in [
        ("stdlib", [{"module": "stdlib", "function": "Vulnerable"}], 1),
        ("example.com/module", [{"module": "example.com/module", "function": "Vulnerable"}], 1),
        ("uncalled", [{"module": "example.com/module"}], 0),
        ("empty", [], 0),
        ("missing", None, 0),
    ]:
        finding = {"osv": "GO-TEST", "fixed_version": "v1.2.3"}
        if trace is not None:
            finding["trace"] = trace
        path.write_text(json.dumps({"finding": finding}))
        result = subprocess.run([sys.executable, str(report), str(path)], capture_output=True, text=True)
        assert result.returncode == expected, (module, result)
        assert "govulncheck:" in result.stdout
    path.write_text("invalid JSON")
    assert subprocess.run([sys.executable, str(report), str(path)], capture_output=True).returncode != 0
print("govulncheck report checks passed")
