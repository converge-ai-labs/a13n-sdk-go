"""Generated Go operations follow the exact pinned OpenAPI contract."""

import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("sdk_codegen", ROOT / "codegen/generate.py")
assert SPEC and SPEC.loader
codegen = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(codegen)


def operations(document: dict) -> set[tuple[str, str, str]]:
    return {
        (method.upper(), path, action["operationId"])
        for path, methods in document["paths"].items()
        for method, action in methods.items()
        if method in {"get", "post", "put", "patch", "delete"}
    }


def test_changed_http_contract_regenerates_bindings(tmp_path: Path) -> None:
    document = {
        "openapi": "3.1.0",
        "info": {"title": "Autogen fixture", "version": "1"},
        "components": {"schemas": {"RunStatus": {"type": "string", "enum": ["queued", "running"]}}},
        "paths": {
            "/api/v1/autogen-probe": {
                "get": {
                    "operationId": "autogen_probe",
                    "description": "Read the future probe.",
                    "parameters": [
                        {
                            "name": "autogen_probe_value",
                            "in": "query",
                            "description": "Select a future probe value.",
                            "schema": {"type": "string"},
                        }
                    ],
                    "responses": {"204": {"description": "No content"}},
                }
            }
        },
    }
    output = codegen.generate(document, tmp_path)
    target = tmp_path / "installed"
    target.mkdir()
    (target / "obsolete.txt").write_text("old generated output")
    codegen.install(output, target)
    assert not (target / "obsolete.txt").exists()
    bindings = (target / "client.gen.go").read_text()
    assert "AutogenProbe" in bindings
    assert "autogen_probe_value" in bindings
    assert "GET /api/v1/autogen-probe" in bindings
    assert not (target / "resources.gen.go").exists()


def test_skill_sync_factoring_keeps_the_wire_schema() -> None:
    document = json.loads((ROOT / "contract/openapi.json").read_text())
    original = json.loads(json.dumps(document))
    codegen.name_skill_sync(document)
    schemas = document["components"]["schemas"]
    policy = schemas.pop("SkillSync")
    for name in ("AgentConfig-Input", "AgentConfig-Output"):
        assert schemas[name]["properties"]["skill_sync"] == {"$ref": "#/components/schemas/SkillSync"}
        schemas[name]["properties"]["skill_sync"] = dict(policy, title="Skill Sync")
    for name in ("AgentOverride-Input", "AgentOverride-Output"):
        choices = schemas[name]["properties"]["skill_sync"]["anyOf"]
        assert choices[0] == {"$ref": "#/components/schemas/SkillSync"}
        choices[0] = policy.copy()
    assert document == original


def test_generated_operations_are_exactly_pinned_contract() -> None:
    """Compare operation identity, method and path, not a count-only inventory."""
    document = json.loads((ROOT / "contract/openapi.json").read_text())
    generated = (ROOT / "generated/client.gen.go").read_text()
    assert "type MessageHistory = []json.RawMessage" in generated
    assert "nullable.Nullable[SkillSync]" in generated
    assert "func (t SkillSync) MarshalJSON()" in generated
    expected = {
        (verb, path, "".join(part[:1].upper() + part[1:] for part in operation_id.split("_")))
        for verb, path, operation_id in operations(document)
    }
    import re

    actual = set(
        re.findall(r"Corresponds with (GET|POST|PUT|PATCH|DELETE) (\S+) \(the `([^`]+)` operationId\)", generated)
    )
    assert actual == expected
    for _, _, operation_id in expected:
        assert re.search(rf"func \(c \*Client\) {operation_id}(WithBody)?\(", generated)
    for schema_name in document["components"]["schemas"]:
        # oapi-codegen removes input/output separators and exports a leading
        # underscore as Underscore (the nested display continuation schemas).
        name = schema_name.replace("-", "")
        if name.startswith("_"):
            name = "Underscore" + name[1:]
        assert re.search(rf"^type {name}\b", generated, re.MULTILINE)


def test_both_generated_graphs_match_pinned_inputs(tmp_path: Path) -> None:
    """CI checks deterministic HTTP and Session outputs, including new files."""
    import re

    document = json.loads((ROOT / "contract/openapi.json").read_text())
    schema = json.loads((ROOT / "contract/session-stream.schema.json").read_text())
    output = codegen.generate(document, tmp_path)
    codegen.generate_session(schema, tmp_path, output / "sessionstream")
    actual = {path.relative_to(output): path.read_bytes() for path in output.rglob("*.go")}
    expected = {path.relative_to(ROOT / "generated"): path.read_bytes() for path in (ROOT / "generated").rglob("*.go")}
    assert actual == expected
    text = (output / "sessionstream/models.gen.go").read_text()
    assert set(schema["frames"]) == {"thread", "items", "delta", "gap", "schedule", "occurrence"}
    bundled = (output / "sessionstream/schema.gen.go").read_text().split("const JSONSchema = ", 1)[1].strip()
    assert json.loads(json.loads(bundled)) == schema
    assert "nullable.Nullable[SkillSync]" in text
    assert "func (t SkillSync) MarshalJSON()" in text
    assert "type JsonValue = json.RawMessage" in text
    for name in schema["$defs"]:
        go_name = "Underscore" + name[1:] if name.startswith("_") else name
        assert re.search(rf"^type {go_name}\b", text, re.MULTILINE)
