<!--
What the model reads about this plugin's tools — in English; the same text in Russian is
tools.ru.md. The format is the connector's (internal/tooltext): "# tool", "## parameter";
adjacent lines join with a space, a blank line starts a new line of the text.
-->

# web_search
Search the public internet. Reach for it when the answer depends on information that is current,
external or absent from the project and the knowledge base: a library's release notes, an error
message, a standard, a vendor's documentation, a price, a date. Returns titles, addresses and
snippets; read a promising one with web_fetch. Results the administrator's lists exclude are not
shown.

## query
What to search for, as you would type it

# web_fetch
Read a public web page by address — documentation, an article, a README, a JSON answer. Use it the
moment you have an address: from the task, from the conversation or from web_search. HTML comes back
as text with headings, lists, tables and links kept; the whole page, never trimmed. An address the
administrator's lists exclude, an internal address, or a content type outside the allowed ones is
refused with the reason.

## url
Full address, with http:// or https://
