# Process flow — `0001-fix-ci-integration-proxy`

Planning → execution flow for this promoted run. Branch `fix/ci-integration-proxy`; the prototype commits `7c1ab05` (fix) and `36ecaf2` (repro) are already on the branch and are the **change under test**, not a proposal to redo.

```mermaid
flowchart TD
    subgraph PLAN["Planning · 0001-fix-ci-integration-proxy"]
        A["issue.md approved<br/>CI-only race · Integration red on main since Oct 9"]
        B["brainstormer + architect<br/>fix 7c1ab05 stays as committed"]
        C["behavior.feature · 10 scenarios<br/>bounded wait + degradation contract"]
        D["plan.md · adr_required: true<br/>amend ADR-0013 (M19 + §6) · SKILL.md clause"]
        E["context.md · codegraph ready @36ecaf2"]
        A --> B --> C --> D --> E
    end
    subgraph EXEC["Execution expected"]
        F["Verify prototype locally<br/>forced-polling repro · CI-equivalent suite"]
        G["Doc edges: ADR-0013 measured row · SKILL.md clause"]
        H["Gates: make check · mutate-diff · coverage 100%"]
        I["PR → Integration green → merge<br/>→ main push green → make install"]
        F --> G --> H --> I
    end
    E --> F
    D -.-> R1["Risk: 3.5 s window vs 3 s polling fallback<br/>(~500 ms slip margin — watch the PR run)"]
    D -.-> R2["Risk: timing-only mutation survivors in apply.go<br/>kill with a timing-tolerant test, never a silent allowlist"]
    A -.-> S1["Scope cuts: no portless pin change · no refactor<br/>mutation files frozen · ADR-0014 is a separate run"]
```
