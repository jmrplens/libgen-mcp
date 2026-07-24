# libgen-mcp live LLM eval results

Model: `claude-haiku-4-5-20251001`

| Scenario | Mode | Status | Detail |
| --- | --- | --- | --- |
| S1 | local | PASS | nonfiction search; 25 results; first md5 ok |
| S2 | local | PASS | articles search; found a result with a valid DOI |
| S3 | local | PASS | standards search; 25 results |
| S4 | local | PASS | get_details returned a File or Edition record |
| S5 | local | PASS | downloaded 982888 bytes via libgen |
| S6 | local | PASS | downloaded 6460651 bytes via scihub |
| S6b | local | PASS | downloaded 982888 bytes via randombook |
| S7 | local | PASS | downloaded DOI via unpaywall |
| S8 | local | PASS | model asked to clarify instead of guessing (no tool call) |
| S9 | local | PASS | start-retries exhausted; actionable error surfaced and the model did not fabricate success |
| S10 | local | PASS | unguided search; 25 results; topics=[fiction] |
| S11 | local | PASS | unguided search; 25 results; topics=[comics] |
| S12 | local | PASS | downloaded 2404614 bytes via libgen |
| S13 | local | PASS | downloaded 6460651 bytes via scihub (doi via search) |
| S14 | local | PASS | received 16 progress notification(s); final progress=982888 total=982888 |
| S15 | local | PASS | ordered page of 100 with links; model surfaced links in its answer |
| S16 | local | PASS | resolved a URL via libgen without downloading: https://libgen.li/get.php?(query redacted) |
| S17 | remote | PASS | remote: model got a link, harness fetched 982888 bytes to local disk |
| S18 | remote | PASS | remote: model got a link and the server returned it; the harness's own fetch was refused upstream (HTTP 403) |
| S19 | local | PASS | the file was not extractable (no extractable text layer (likely a scanned or image-only PDF); OCR is not supported); the model reported that plainly instead of inventing a result |
| S20 | local | PASS | open-access discovery surfaced 17 hit(s); model referenced one in its answer |
| S21 | local | PASS | model searched, called get_details, and surfaced the returned BibTeX citation |
| S22 | local | PASS | model set enrich=true; Crossref journal="Cell" citations=56388; model answered the ask |
| S23 | local | PASS | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S24 | local | PASS | model used read outline=true; 221 table-of-contents entr(ies) returned |
| S25 | local | PASS | DOI download succeeded via scihub (255629 bytes); the host answered any email elicitation the server raised |
| S26 | local | PASS | save-confirmation elicitation fired 1x and the host accepted it; downloaded 4366258 bytes via libgen — confirmation did not block the flow |
| S27 | remote | PASS | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S28 | remote | PASS | no embedded table of contents; the model read the document and compiled one from its text |
| S29 | remote | PASS | open-access discovery surfaced 20 hit(s); model referenced one in its answer |
| S30 | remote | PASS | model set enrich=true; Crossref journal="Cell" citations=56388; model answered the ask |
| S31 | remote | PASS | model searched, called get_details, and surfaced the returned BibTeX citation |
| S32 | local | PASS | escalation surfaced 10 Anna's-origin result(s); model did not report not-found |
| S33 | remote | PASS | escalation surfaced 10 Anna's-origin result(s); model did not report not-found |
| S34 | local | PASS | model searched, found an Anna's-origin item, and downloaded it (md5=00dd2b0b58e81e3c6e7cb9e7b72dee23) |
| S35 | remote | PASS | model searched, found an Anna's-origin item, and downloaded it (md5=00dd2b0b58e81e3c6e7cb9e7b72dee23) |
| S36 | local | PASS | get_details fell back to Anna's for the escalated md5 (collection=zlib) |
| S37 | remote | PASS | get_details fell back to Anna's for the escalated md5 (collection=zlib) |
| S38 | local | PASS | never mode honored and the model reported the miss honestly |
| S39 | local | PASS | always mode consulted the extras alongside a 29-result catalog page (annas=4, open access=28) |
| S40 | local | PASS | read opened an Anna's-only item (1536 chars extracted) |
| S41 | local | PASS | member download reported the account allowance (45 of 50 left) |
| S42 | local | PASS | nothing exists by that name and the model said so, inventing no metadata |
| S43 | local | PASS | restriction held; the model routed through the permitted source instead of the refused one |
| S44 | local | PASS | model set page=2 and received page 2 with 25 results |
