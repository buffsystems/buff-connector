<!--
What the model reads about the organisation's knowledge base tools (#371) — in English; the same
text in Russian is knowledge.ru.md. The format is internal/tooltext: "# tool", "## parameter";
adjacent lines join with a space, a blank line starts a new line of the text. Buff Code's Code Mode
shows only a description's first line, cut at 120 characters: it carries what the tool does and
when to call it.
-->

# list_entries
List this context's knowledge base entries, without bodies. Call it before write_entry to avoid a duplicate.

Each entry: name, title, one-line description, type, scope and author. An entry with
always_in_prompt is already given in full in your prompt.

# read_entry
Read a knowledge base entry by its name from the index. Call it as soon as an index line answers the question.

Do it before inventing an answer or digging into the code. Returns {found, body, scope, author};
for an entry already given in full in the prompt, the body is not repeated (inlined: true).

## name
the entry's name (in Latin letters, from the knowledge index)

# write_entry
Add an entry to the knowledge base: the call itself asks the user with a form. Don't write "adding it" — call.

Call it THE MOMENT the user asks you to remember something or states a rule, when you have
established a lasting fact about the environment or the project, or hit a reproducible obstacle
and found a way around it. Nothing is written without the user's "yes"; written: false — they
declined, do not offer this entry again. scope: org (the whole organisation) or the name of a
project or space from the work context.

## name
the entry's address-name in Latin letters, words joined by hyphens

## title
a title for a person

## description
one line: what the entry is about — by it a model decides whether to read the body

## type
rule — a working rule; fact — how things are set up here; pitfall — an obstacle and the way around it

## scope
org, or the name of a project/space from the work context

## body
the body in markdown: what exactly there is to know
