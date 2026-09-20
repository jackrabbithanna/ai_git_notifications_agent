import { useCallback, useEffect, useState } from "react";
import { ProfilesService } from "../../bindings/ghinbox/internal/services";
import type { Profile } from "../../bindings/ghinbox/internal/profiles";
import { Button, Card, Chip, ErrorText, errMsg } from "../lib/ui";

// Profiles: built-in and user impact profiles. Editing a built-in saves a user
// copy with the same id, which overrides it; deleting the copy restores the built-in.
export default function Profiles() {
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const [problems, setProblems] = useState<string[]>([]);
  const [error, setError] = useState("");
  const [yaml, setYaml] = useState("");
  const [editing, setEditing] = useState<string>("");
  const [validated, setValidated] = useState<Profile | null>(null);
  const [notice, setNotice] = useState("");

  const load = useCallback(() => {
    ProfilesService.List()
      .then((v) => {
        setProfiles(v.profiles ?? []);
        setProblems(v.problems ?? []);
      })
      .catch((e) => setError(errMsg(e)));
  }, []);
  useEffect(() => {
    load();
  }, [load]);

  const edit = (p: Profile) => {
    setEditing(p.id);
    setYaml(p.yaml);
    setValidated(null);
    setNotice("");
  };
  const validate = () => {
    ProfilesService.Validate(yaml)
      .then((p) => {
        setValidated(p);
        setError("");
        setNotice(`Valid: ${p.id} (${p.name}), ${p.repos?.length ?? 0} repos, ${p.layers?.length ?? 0} layers, ${p.surfacePatterns?.length ?? 0} surface patterns.`);
      })
      .catch((e) => {
        setValidated(null);
        setError(errMsg(e));
      });
  };
  const save = () => {
    ProfilesService.Save(yaml, true)
      .then((p) => {
        setNotice(`Saved user profile ${p.id}.`);
        setError("");
        load();
      })
      .catch((e) => setError(errMsg(e)));
  };
  const toggle = (p: Profile) => ProfilesService.SetEnabled(p.id, !p.enabled).then(load).catch((e) => setError(errMsg(e)));
  const remove = (p: Profile) => {
    if (!window.confirm(`Delete user profile ${p.id}? A built-in with the same id becomes active again.`)) return;
    ProfilesService.Delete(p.id)
      .then(() => {
        if (editing === p.id) {
          setEditing("");
          setYaml("");
        }
        load();
      })
      .catch((e) => setError(errMsg(e)));
  };
  const blank = () => {
    setEditing("new");
    setValidated(null);
    setNotice("");
    setYaml(`id: my-project
name: My Project
repos:
  - owner/name            # GitHub
  - gitlab:lab.example.org/group/project
downstream_description: >
  What I build on this project and which surfaces matter to me.
layers:
  - id: api
    label: Public API
    weight: 1.0
    paths: ["src/api/**"]
  - id: core
    label: Core
    weight: 0.7
    paths: ["src/**"]
ignore_paths: ["tests/**", "**/*.md"]
signals: ["BREAKING", "deprecat", "remove"]
surface_patterns:
  - id: exported
    pattern: '^[+-]\\s*(export|public)\\s'
`);
  };

  return (
    <div className="space-y-4">
      {error && <ErrorText>{error}</ErrorText>}
      {problems.map((p) => (
        <ErrorText key={p}>profile problem: {p}</ErrorText>
      ))}
      <Card
        title="Impact profiles"
        actions={
          <Button kind="primary" onClick={blank}>
            New profile
          </Button>
        }
      >
        <ul className="divide-y divide-neutral-200 dark:divide-neutral-800">
          {profiles.map((p) => (
            <li key={p.id} className="flex flex-wrap items-center gap-3 py-2 text-sm">
              <span className="font-medium">{p.name}</span>
              <span className="font-mono text-xs text-neutral-500">{p.id}</span>
              <Chip tone={p.source === "user" ? "blue" : "neutral"}>{p.source}</Chip>
              <span className="text-xs text-neutral-500">
                {p.repos?.length ?? 0} repos · {p.layers?.length ?? 0} layers · {p.surfacePatterns?.length ?? 0} patterns
              </span>
              <label className="flex items-center gap-1 text-xs">
                <input type="checkbox" checked={p.enabled} onChange={() => toggle(p)} /> enabled
              </label>
              <span className="ml-auto flex gap-1">
                <Button onClick={() => edit(p)}>{p.source === "user" ? "Edit" : "Copy & edit"}</Button>
                {p.source === "user" && (
                  <Button kind="danger" onClick={() => remove(p)}>
                    Delete
                  </Button>
                )}
              </span>
            </li>
          ))}
        </ul>
      </Card>
      {editing && (
        <Card
          title={editing === "new" ? "New profile (YAML)" : `Edit ${editing} (saved as a user profile)`}
          actions={
            <>
              <Button onClick={validate}>Validate</Button>
              <Button kind="primary" onClick={save} disabled={!yaml.trim()}>
                Save
              </Button>
              <Button kind="ghost" onClick={() => setEditing("")}>
                Close
              </Button>
            </>
          }
        >
          <textarea
            className="h-96 w-full rounded border border-neutral-300 bg-white p-2 font-mono text-xs dark:border-neutral-700 dark:bg-neutral-900"
            value={yaml}
            onChange={(e) => setYaml(e.target.value)}
            spellCheck={false}
          />
          {notice && <p className="mt-2 text-xs text-green-700 dark:text-green-300">{notice}</p>}
          {validated && (
            <p className="mt-1 text-xs text-neutral-500">Layers: {validated.layers?.map((l) => `${l.id} (${l.weight})`).join(", ")}</p>
          )}
        </Card>
      )}
    </div>
  );
}
