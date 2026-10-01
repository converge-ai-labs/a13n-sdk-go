"""Exercise a module ZIP through a local Go proxy, never a source replacement.

Without --offline, run the consumer against an explicitly configured disposable
HTTPS Service. This creates no remote release and provisions no infrastructure.
"""

import argparse
import json
import os
import shutil
import subprocess
import tempfile
import zipfile
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
MODULE = "github.com/converge-ai-labs/a13n-sdk-go"
VERSION = "v0.0.0"


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--offline", action="store_true")
    args = parser.parse_args()
    with tempfile.TemporaryDirectory(prefix="a13n-go-consumer-") as directory:
        temp = Path(directory)
        proxy = temp / "proxy"
        versions = proxy / MODULE / "@v"
        versions.mkdir(parents=True)
        (versions / f"{VERSION}.mod").write_bytes((ROOT / "go.mod").read_bytes())
        (versions / f"{VERSION}.info").write_text(json.dumps({"Version": VERSION, "Time": "2026-01-01T00:00:00Z"}))
        (versions / "list").write_text(VERSION + "\n")
        sources = [ROOT / name for name in ("go.mod", "go.sum", "LICENSE", "README.md")]
        sources += [file for file in ROOT.glob("*.go") if not file.name.endswith("_test.go")]
        sources += list((ROOT / "generated").glob("*.go"))
        with zipfile.ZipFile(versions / f"{VERSION}.zip", "w", zipfile.ZIP_DEFLATED) as archive:
            for source in sources:
                archive.write(source, f"{MODULE}@{VERSION}/{source.relative_to(ROOT)}")
        consumer = temp / "consumer"
        consumer.mkdir()
        for source in (ROOT / "scripts" / "acceptance").glob("*.go"):
            shutil.copyfile(source, consumer / source.name)
        (consumer / "go.mod").write_text(
            f"module example.invalid/sdk-consumer\n\ngo 1.25.0\n\nrequire {MODULE} {VERSION}\n"
        )
        shutil.copyfile(ROOT / "go.sum", consumer / "go.sum")
        environment = {
            **os.environ,
            "GOWORK": "off",
            "GOPROXY": f"{proxy.as_uri()},https://proxy.golang.org",
            "GONOPROXY": "none",
            "GONOSUMDB": MODULE,
            "GOMODCACHE": str(temp / "modules"),
        }
        subprocess.run(
            ["go", "run", "-mod=mod", ".", *(["--offline"] if args.offline else [])],
            cwd=consumer,
            env=environment,
            check=True,
        )
        if "replace " in (consumer / "go.mod").read_text():
            raise AssertionError("Consumer must resolve the module ZIP, not source replacement")
        print("Isolated Go module ZIP consumer passed", flush=True)


if __name__ == "__main__":
    main()
