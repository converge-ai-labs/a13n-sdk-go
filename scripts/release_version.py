from __future__ import annotations

import re
from dataclasses import dataclass
from pathlib import Path

COMPONENTS = ("a13n-go",)

RELEASE_VERSION_PATTERN = re.compile(
    r"(?P<major>0|[1-9][0-9]*)\."
    r"(?P<minor>0|[1-9][0-9]*)\."
    r"(?P<patch>0|[1-9][0-9]*)"
    r"(?:-rc\.(?P<rc>[1-9][0-9]*))?"
)


class ReleaseVersionError(ValueError):
    pass


@dataclass(frozen=True)
class ReleaseVersion:
    major: int
    minor: int
    patch: int
    rc: int | None

    @property
    def canonical(self) -> str:
        base = f"{self.major}.{self.minor}.{self.patch}"
        if self.rc is None:
            return base
        return f"{base}-rc.{self.rc}"

    @property
    def python_package(self) -> str:
        if self.rc is None:
            return self.canonical
        return f"{self.major}.{self.minor}.{self.patch}rc{self.rc}"

    @property
    def is_prerelease(self) -> bool:
        return self.rc is not None

    @property
    def precedence_key(self) -> tuple[int, int, int, int, int]:
        if self.rc is None:
            return self.major, self.minor, self.patch, 1, 0
        return self.major, self.minor, self.patch, 0, self.rc


def parse_release_version(version: str) -> ReleaseVersion:
    match = RELEASE_VERSION_PATTERN.fullmatch(version)
    if match is None:
        raise ReleaseVersionError(f"Release version must use X.Y.Z or X.Y.Z-rc.N syntax: {version}")
    rc = match.group("rc")
    return ReleaseVersion(
        major=int(match.group("major")),
        minor=int(match.group("minor")),
        patch=int(match.group("patch")),
        rc=int(rc) if rc is not None else None,
    )


def validate_version_syntax(version: str) -> None:
    parse_release_version(version)


def component_versions(root: Path, component: str) -> dict[str, str]:
    if component not in COMPONENTS:
        raise ReleaseVersionError(f"Unknown release component: {component}")
    return {}


def validate_component_version(root: Path, component: str, version: str) -> None:
    release = parse_release_version(version)
    component_versions(root, component)
    module = re.search(r"(?m)^module\s+(\S+)\s*$", (root / "go.mod").read_text())
    suffix = f"/v{release.major}" if release.major >= 2 else ""
    expected = f"github.com/converge-ai-labs/a13n-sdk-go{suffix}"
    if module is None or module[1] != expected:
        raise ReleaseVersionError(
            f"Release {version} requires Go module {expected}; review module/import changes first"
        )


def prepare_component_version(root: Path, component: str, version: str) -> tuple[Path, ...]:
    validate_component_version(root, component, version)
    return ()
