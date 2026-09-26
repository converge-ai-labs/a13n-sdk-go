"""Generator adapters and generated binding behavior."""

import importlib.util
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("sdk_codegen", ROOT / "codegen/generate.py")
assert SPEC and SPEC.loader
codegen = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(codegen)
RESOURCE_SPEC = importlib.util.spec_from_file_location("sdk_resources", ROOT / "codegen/resources.py")
assert RESOURCE_SPEC and RESOURCE_SPEC.loader
resources = importlib.util.module_from_spec(RESOURCE_SPEC)
RESOURCE_SPEC.loader.exec_module(resources)


def test_changed_http_contract_regenerates_bindings(tmp_path: Path) -> None:
    """Exercise the real pinned generator, not the sync test's fake make."""
    document = {
        "openapi": "3.1.0",
        "info": {"title": "Autogen fixture", "version": "1"},
        "components": {"schemas": {"RunStatus": {"type": "string", "enum": ["queued", "running"]}}},
        "paths": {
            "/api/v1/autogen-probe": {
                "get": {
                    "operationId": "autogen_probe",
                    "description": "Read the future probe.",
                    "responses": {"204": {"description": "No content"}},
                }
            }
        },
    }
    document["paths"]["/api/v1/autogen-probe"]["get"]["parameters"] = [
        {
            "name": "autogen_probe_value",
            "in": "query",
            "description": "Select a future probe value.",
            "schema": {"type": "string"},
        }
    ]
    output = codegen.generate(document, tmp_path)
    target = tmp_path / "installed"
    target.mkdir()
    (target / "obsolete.txt").write_text("old generated output")
    codegen.install(output, target)
    assert not (target / "obsolete.txt").exists()
    bindings = (target / "client.gen.go").read_text()
    assert "autogen_probe_value" in bindings
    resources.generate_resources(document, bindings, tmp_path)
    ordinary = (tmp_path / "resources.gen.go").read_text()
    assert "// Get calls GET /api/v1/autogen-probe. Read the future probe." in ordinary
    assert "Select a future probe value." in ordinary
    assert "Nil omits this parameter." in " ".join(ordinary.replace("//", "").split())
    assert "\n\nfunc (r AutogenProbeResource) Get" not in ordinary


def test_resource_docs_and_domain_enums_follow_contract(tmp_path: Path) -> None:
    document = json.loads((ROOT / "contract/openapi.json").read_text())
    parameters = document["paths"]["/api/v1/provider-types/{kind}"]["get"]["parameters"]
    kind = next(param for param in parameters if param["name"] == "kind")
    kind["schema"]["enum"].append("future_provider")
    resources.generate_resources(document, (ROOT / "generated/client.gen.go").read_text(), tmp_path)
    text = (tmp_path / "resources.gen.go").read_text()
    assert "type ProviderKind = generated.ListProviderTypes" in text
    assert 'ProviderKindFutureProvider ProviderKind = "future_provider"' in text
    assert "Ref(id ProviderKind)" in text
    assert "Kind *MemberKind" in text
    assert "Source *SkillSource" in text
    assert "Returns the successor Run." in text
    assert "ThreadResource.Events" in text
    assert "Required, caller-chosen request key." in text
    assert "Supply the current resource ETag" in text
    assert "Options are snapshotted" in text
