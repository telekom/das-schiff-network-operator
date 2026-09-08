# SPDX-FileCopyrightText: 2026 Deutsche Telekom AG
# SPDX-License-Identifier: Apache-2.0

"""Run the workflow summary for complete and incomplete version checks."""

import os
from pathlib import Path
import subprocess
import tempfile
import textwrap

workflow = Path(__file__).resolve().parents[1] / ".github/workflows/tool-updates.yaml"
script = textwrap.dedent(workflow.read_text().split("      - name: Summary\n        run: |\n", 1)[1])
script = script.replace("${{ steps.versions.outputs.skip }}", "false")

for failed in ("true", "false"):
    with tempfile.TemporaryDirectory() as directory:
        summary = Path(directory) / "summary.md"
        subprocess.run(
            ["bash", "-e", "-c", script.replace("${{ steps.check.outputs.check_failed }}", failed)],
            cwd=directory,
            env={**os.environ, "GITHUB_STEP_SUMMARY": str(summary)},
            check=True,
        )
        output = summary.read_text()
        assert ("All tools are up to date!" in output) == (failed == "false"), output
        assert ("results are incomplete" in output) == (failed == "true"), output
