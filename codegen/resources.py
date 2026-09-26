"""Generate the ordinary resource surface over oapi-codegen's typed operations."""

import json
import re
from pathlib import Path

VERBS = {"get": "Get", "post": "Create", "put": "Replace", "patch": "Update", "delete": "Delete"}
CORE = {
    "workspaces/{workspace_id}": "WorkspaceResource",
    "workspaces/{workspace_id}/threads": "ThreadsResource",
    "workspaces/{workspace_id}/threads/{thread_id}": "ThreadResource",
    "workspaces/{workspace_id}/threads/{thread_id}/inbox": "InboxResource",
    "workspaces/{workspace_id}/threads/{thread_id}/inbox/{entry_id}": "EntryResource",
    "workspaces/{workspace_id}/runs/{run_id}": "RunResource",
}
OPTIONAL_MATCH = "/api/v1/workspaces/{workspace_id}/memories/{memory_id}/revisions/{seq}/restore"


def pascal(value: str) -> str:
    return "".join(word[0].upper() + word[1:] for word in re.split(r"[^a-zA-Z0-9]+", value) if word)


def qualify(value: str) -> str:
    return re.sub(r"(?<![\w.])([A-Z][A-Za-z0-9]*)\b", r"generated.\1", value)


def resource_name(path: str) -> str:
    if path in CORE:
        return CORE[path]
    parts = []
    for segment in path.split("/"):
        if segment.startswith("{"):
            word = parts.pop()
            parts.append(word[:-3] + "y" if word.endswith("ies") else word[:-1] if word.endswith("s") else word)
        else:
            parts.append(segment)
    if (
        len(parts) > 1
        and parts[0] == "workspace"
        and parts[1] not in {"connection", "connections", "invitation", "invitations"}
    ):
        parts = parts[1:]
    return "".join(pascal(part) for part in parts) + "Resource"


def generate_resources(document: dict, bindings: str, target: Path) -> None:
    nodes: dict[str, dict] = {"": {"children": {}, "ops": [], "name": "ServiceResources"}}
    operations = []
    for path, item in document["paths"].items():
        relative = path.removeprefix("/api/v1/") if path.startswith("/api/v1/") else path.lstrip("/")
        parent = ""
        for segment in relative.split("/"):
            key = f"{parent}/{segment}".lstrip("/")
            nodes[parent]["children"][segment] = key
            nodes.setdefault(key, {"children": {}, "ops": [], "name": resource_name(key)})
            parent = key
        for verb, operation in item.items():
            if verb not in VERBS:
                continue
            name = pascal(operation["operationId"])
            match = re.search(r"func \(c \*Client\) " + name + r"\((.*?)\) \(\*http.Response, error\)", bindings)
            # Raw non-JSON requests have only WithBody methods.
            if not match:
                match = re.search(
                    r"func \(c \*Client\) " + name + r"WithBody\((.*?)\) \(\*http.Response, error\)", bindings
                )
            assert match, name
            op = dict(operation, verb=verb, path=path, relative=relative, name=name, signature=match[1])
            op["parameters"] = item.get("parameters", []) + operation.get("parameters", [])
            nodes[parent]["ops"].append(op)
            operations.append(op)
    for key, node in nodes.items():
        node["flatten"] = bool(
            key
            and not key.endswith("}")
            and not key.endswith("s")
            and not node["children"]
            and len(node["ops"]) == 1
            and node["ops"][0]["verb"] == "post"
        )

    names = [node["name"] for node in nodes.values()]
    assert len(set(names)) == len(names), "resource name collision"
    declarations = []
    methods = []
    coverage = []
    tests = []
    for key, node in nodes.items():
        if node["flatten"]:
            continue
        name = node["name"]
        declarations.append(f"type {name} struct {{ binding }}")
        for segment, child_key in node["children"].items():
            child = nodes[child_key]
            if child["flatten"]:
                continue
            if segment.startswith("{"):
                parameter = segment[1:-1]
                sample = next(
                    op for op in operations if op["relative"] == child_key or op["relative"].startswith(child_key + "/")
                )
                path_parameters = [p for p in sample["parameters"] if p["in"] == "path"]
                index = next(i for i, p in enumerate(path_parameters) if p["name"] == parameter)
                signature = sample["signature"].split(", ")[index + 1]
                value_type = qualify(signature.split(" ", 1)[1])
                methods.append(
                    f"func (r {name}) Ref(id {value_type}) {child['name']} {{ return {child['name']}{{r.selectID(fmt.Sprint(id))}} }}"
                )
            else:
                methods.append(
                    f"func (r {name}) {pascal(segment)}() {child['name']} {{ return {child['name']}{{r.binding}} }}"
                )
        own_ops = [(op, VERBS[op["verb"]]) for op in node["ops"]]
        own_ops += [
            (child["ops"][0], pascal(segment))
            for segment, child_key in node["children"].items()
            if (child := nodes[child_key])["flatten"]
        ]
        for op, method in own_ops:
            response_match = re.search(r"type " + op["name"] + r"Response struct \{(.*?)\n\}", bindings, re.S)
            assert response_match, op["name"]
            responses = re.findall(r"JSON(2\d\d)\s+\*([^\n]+)", response_match[1])
            success = [int(code) for code in op["responses"] if code.isdigit() and 200 <= int(code) < 300]
            # Pinned API semantics: OAuth completion may redirect to return_url.
            if op["path"] == "/api/v1/connections/callback" and op["verb"] == "get":
                success.append(303)
            response_types = set(t for _, t in responses)
            assert len(response_types) <= 1, op["name"]
            result_type = qualify(next(iter(response_types))) if response_types else "struct{}"
            response_schema = next(
                (
                    r.get("content", {}).get("application/json", {}).get("schema", {})
                    for code, r in op["responses"].items()
                    if code.startswith("2")
                ),
                {},
            )
            if "$ref" in response_schema:
                response_schema = document["components"]["schemas"][response_schema["$ref"].split("/")[-1]]
            if (
                op["verb"] == "get"
                and "items" in response_schema.get("properties", {})
                and not op["path"].endswith("/runs/{run_id}/items")
            ):
                method = "List"
            option_name = name.removesuffix("Resource") + method + "Options"
            params_match = re.search(r"type " + op["name"] + r"Params struct \{(.*?)\n\}", bindings, re.S)
            fields = (
                re.findall(r"\n\s*(\w+)\s+([^`\n]+?)\s+`[^`]*json:\"([^\",]+)[^`]*`", params_match[1])
                if params_match
                else []
            )
            args = ["ctx context.Context"]
            call_args = ["ctx"]
            path_parameters = [p for p in op["parameters"] if p["in"] == "path"]
            for i, _ in enumerate(path_parameters):
                value_type = op["signature"].split(", ")[i + 1].split(" ", 1)[1]
                call_args.append(
                    f"r.ids[{i}]"
                    if value_type == "string"
                    else f"{qualify(value_type)}(r.integerID({i}))"
                    if value_type == "int"
                    else f"{qualify(value_type)}(r.ids[{i}])"
                )
            checks = []
            option_fields = []
            param_assignments = []
            for field, typ, wire in fields:
                required_match = wire == "If-Match" and op["path"] != OPTIONAL_MATCH
                option_type = "string" if required_match else qualify(typ.strip())
                option_fields.append(f"{field} {option_type}")
                if required_match or (wire == "Idempotency-Key"):
                    checks.append(f'if options.{field} == "" {{ return zero, fmt.Errorf("{wire} is required") }}')
                value = f"&options.{field}" if required_match and typ.strip() == "*string" else f"options.{field}"
                param_assignments.append(f"{field}: {value}")
            if fields:
                call_args.append(f"&generated.{op['name']}Params{{{', '.join(param_assignments)}}}")
            media = op.get("requestBody", {}).get("content", {})
            binary = bool(media) and "application/json" not in media and "multipart/form-data" not in media
            multipart = "multipart/form-data" in media
            prelude = []
            call_name = op["name"]
            if "application/json" in media:
                body_alias = re.search(r"type " + op["name"] + r"JSONRequestBody = ([^\n]+)", bindings)
                body_type = qualify(body_alias[1]) if body_alias else "generated." + op["name"] + "JSONRequestBody"
                args.append(f"body {body_type}")
                call_args.append("body")
            elif binary:
                args.append("body io.Reader")
                option_fields.append("ContentType string")
                media_values = ", ".join(json.dumps(m) for m in media)
                checks.append(
                    f'if !slices.Contains([]string{{{media_values}}}, options.ContentType) {{ return zero, fmt.Errorf("unsupported image content type") }}'
                )
                call_name += "WithBody"
                checks.append('if body == nil { return zero, fmt.Errorf("image reader is required") }')
                call_args += ["options.ContentType", "readerOnly{body}"]
            elif multipart:
                args.append("file UploadFile")
                prelude += ["contentType, body, err := multipartBody(file)", "if err != nil { return zero, err }"]
                call_name += "WithBody"
                call_args += ["contentType", "body"]
            if option_fields:
                declarations.append(f"type {option_name} struct {{ {'; '.join(option_fields)} }}")
                args.append(f"options {option_name}")
            stream = not response_types and any(
                r.get("content") for code, r in op["responses"].items() if code.startswith("2")
            )
            submitted = result_type == "generated.Submitted"
            public_result = "*BinaryResult" if stream else "*Submitted" if submitted else f"Result[{result_type}]"
            call = f"r.client.api.{call_name}({', '.join(call_args)})"
            code = [
                f"func (r {name}) {method}({', '.join(args)}) ({public_result}, error) {{",
                f"var zero {public_result}",
                "if err := r.validate(); err != nil { return zero, err }",
                *checks,
                *prelude,
                f"response, err := {call}",
            ]
            codes = ", ".join(map(str, success))
            if stream:
                code.append(f"return binaryResult(r.client, response, err, {codes})")
            elif submitted:
                code += [
                    f"receipt, err := jsonResult[{result_type}](r.client, response, err, {codes})",
                    "if err != nil { return nil, err }",
                    "return bindSubmitted(r.client, receipt)",
                ]
            else:
                code.append(f"return jsonResult[{result_type}](r.client, response, err, {codes})")
            code.append("}")
            methods.append("\n".join(code))
            coverage.append(f'"{op["verb"].upper()} {op["path"]}": "{name}.{method}"')
            # Executable coverage of each public resource call, not just emitted names.
            test_binding = "client.Resources()"
            parts = key.split("/") if key else []
            for segment in parts:
                if segment.startswith("{"):
                    param = next(p for p in path_parameters if p["name"] == segment[1:-1])
                    schema = param["schema"]
                    test_binding += (
                        ".Ref(7)"
                        if schema.get("type") == "integer"
                        else '.Ref("memory")'
                        if segment == "{kind}"
                        else '.Ref("part /雪%")'
                    )
                else:
                    test_binding += f".{pascal(segment)}()"
            test_args = ["context.Background()"]
            if "application/json" in media:
                schema = media["application/json"]["schema"]
                if "$ref" in schema:
                    schema = document["components"]["schemas"][schema["$ref"].split("/")[-1]]
                body_fields = [
                    f'{pascal(field)}: "user@example.test"'
                    for field, spec in schema.get("properties", {}).items()
                    if spec.get("format") == "email"
                ]
                test_args.append(f"generated.{op['name']}JSONRequestBody{{{', '.join(body_fields)}}}")
            elif binary:
                test_args.append('strings.NewReader("bytes")')
            elif multipart:
                test_args.append(
                    'UploadFile{Name: "sample.txt", ContentType: "text/plain", Reader: strings.NewReader("bytes")}'
                )
            assignments = []
            for field, typ, wire in fields:
                if wire in {"If-Match", "Idempotency-Key"}:
                    assignments.append(
                        f"{field}: "
                        + (
                            'pointer("header")'
                            if typ.strip() == "*string" and wire == "If-Match" and op["path"] == OPTIONAL_MATCH
                            else '"header"'
                        )
                    )
                elif typ.strip() == "string":
                    assignments.append(f'{field}: "query"')
            if binary:
                assignments.append(f"ContentType: {json.dumps(next(iter(media)))}")
            if option_fields:
                test_args.append(f"{option_name}{{{', '.join(assignments)}}}")
            # A permissive skeleton is sufficient for transport coverage, not schema validity.
            payload = (
                '{"thread":{"workspace_id":"ws","id":"thr"},"entry":{"id":"ent"},"run":null}' if submitted else "{}"
            )
            for status in success:
                tests.append(f"""t.Run({json.dumps(op["name"] + str(status))}, func(t *testing.T) {{
                    called := false
                    client := coverageClient(t, func(req *http.Request) *http.Response {{
                        called = true
                        checkCoverageRequest(t, req, {json.dumps(op["verb"].upper())}, {json.dumps(op["path"])})
                        return coverageResponse({status}, {json.dumps(payload)})
                    }})
                    result, err := {test_binding}.{method}({", ".join(test_args)})
                    if err != nil {{ t.Fatal(err) }}
                    {"_ = result.Close()" if stream else "_ = result"}
                    if !called {{ t.Fatal("operation was not dispatched") }}
                }})""")
            paged = any(
                p["name"] == "cursor" and p["in"] == "query" for p in op["parameters"]
            ) and "next_cursor" in response_schema.get("properties", {})
            if paged:
                assert method == "List"
                snapshots = []
                for field, typ, _ in fields:
                    if typ.strip().startswith("*"):
                        value = (
                            f"slices.Clone(*options.{field})" if typ.strip().startswith("*[]") else f"*options.{field}"
                        )
                        snapshots.append(f"if options.{field} != nil {{ value := {value}; options.{field} = &value }}")
                methods.append(f"""func (r {name}) Pages(ctx context.Context, options {option_name}) iter.Seq2[Result[{result_type}], error] {{
                    {"; ".join(snapshots)}
                    return paginate(ctx, options, func(o {option_name}) (*string) {{ return o.Cursor }},
                        func(o *{option_name}, cursor string) {{ o.Cursor = &cursor }},
                        func(ctx context.Context, o {option_name}) (Result[{result_type}], error) {{ return r.List(ctx, o) }},
                        func(value {result_type}) string {{ return value.NextCursor.GetOrEmpty() }})
                }}""")
    header = "// Code generated by codegen/resources.py; DO NOT EDIT.\npackage a13n\n"
    imports = 'import ("context"; "fmt"; "io"; "iter"; "slices"; "time"; "github.com/converge-ai-labs/a13n-sdk-go/generated")\n'
    # time is needed by generated time-range options when present.
    body = "\n".join(declarations + methods)
    if "time." not in body:
        imports = imports.replace('; "time"', "")
    (target / "resources.gen.go").write_text(
        header + imports + body + "\nvar resourceOperations = map[string]string{\n" + ",\n".join(coverage) + ",\n}\n"
    )
    (target / "resources_coverage.gen_test.go").write_text(
        header
        + 'import ("context"; "net/http"; "strings"; "testing"; "github.com/converge-ai-labs/a13n-sdk-go/generated")\nfunc TestEveryResourceOperation(t *testing.T) {\n'
        + "\n".join(tests)
        + "\n}\n"
    )
