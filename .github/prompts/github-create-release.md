Generate the Markdown changelog body for the provided Gitea Runner Lume release.

Rules:
- Use only the supplied previous-tag-to-target-tag git range as release evidence.
- Do not claim changes from earlier releases or infer changes from final project state.
- Focus on operator-visible runner behavior, Lume VM execution, security, compatibility, installation, and release operations.
- Do not include front matter, metadata, a title repeating the version, or code fences.
- Start with one short, polished summary sentence.
- Use concise `##` sections and concrete, past-tense bullets.
- Do not mention the prompt, AI, OpenRouter, or information outside the supplied context.
