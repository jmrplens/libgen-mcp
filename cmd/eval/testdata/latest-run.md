# libgen-mcp live LLM eval results

Model: `claude-haiku-4-5-20251001`

| Scenario | Mode | Status | Measured | Detail |
| --- | --- | --- | --- | --- |
| S1 | local | PASS | 2026-07-25 | nonfiction search; 25 results; first md5 ok |
| S2 | local | PASS | 2026-07-25 | articles search; found a result with a valid DOI |
| S3 | local | PASS | 2026-07-25 | standards search; 25 results |
| S4 | local | PASS | 2026-07-25 | get_details returned a File or Edition record |
| S5 | local | PASS | 2026-07-25 | downloaded 982888 bytes via libgen |
| S6 | local | PASS | 2026-07-25 | downloaded 6460651 bytes via scihub |
| S6b | local | PASS | 2026-07-25 | downloaded 982888 bytes via randombook |
| S7 | local | PASS | 2026-07-25 | downloaded DOI via unpaywall, an open-access provider — the chain preferred a legal copy |
| S8 | local | PASS | 2026-07-25 | model asked to clarify instead of guessing (no tool call) |
| S9 | local | PASS | 2026-07-25 | start-retries exhausted; actionable error surfaced and the model did not fabricate success |
| S10 | local | PASS | 2026-07-25 | unguided search; 25 results; topics=[fiction] |
| S11 | local | PASS | 2026-07-25 | unguided search; 25 results; topics=[comics] |
| S12 | local | PASS | 2026-07-25 | downloaded 2404614 bytes via libgen |
| S13 | local | PASS | 2026-07-25 | downloaded 2173792 bytes via fatcat (doi via search) |
| S14 | local | PASS | 2026-07-25 | received 17 progress notification(s); final progress=982888 total=982888 |
| S15 | local | PASS | 2026-07-25 | ordered page of 107 with links; model surfaced links in its answer |
| S16 | local | PASS | 2026-07-25 | resolved a URL via libgen without downloading: https://libgen.li/get.php?(query redacted) |
| S17 | remote | PASS | 2026-07-25 | remote: model got a link, harness fetched 982888 bytes to local disk |
| S18 | remote | PASS | 2026-07-25 | remote: model got a link and the server returned it; the harness's own fetch was refused upstream (Get "/article/S0092867411001279/pdf": stopped after 10 redirects) |
| S19 | local | PASS | 2026-07-25 | read pdf (4566 chars); model summarized it in 791 chars |
| S20 | local | PASS | 2026-07-25 | open-access discovery surfaced 41 hit(s); model referenced one in its answer |
| S21 | local | PASS | 2026-07-25 | model searched, called get_details, and surfaced the returned BibTeX citation |
| S22 | local | PASS | 2026-07-25 | model set enrich=true; Crossref journal="Cell" citations=56401; model answered the ask |
| S23 | local | PASS | 2026-07-25 | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S24 | local | PASS | 2026-07-25 | no embedded table of contents; the model read the document and compiled one from its text |
| S25 | local | PASS | 2026-07-25 | the server asked for a contact email it had none of, the host supplied one, and unpaywall served 255629 bytes |
| S26 | local | PASS | 2026-07-25 | save-confirmation elicitation fired 1x and the host accepted it; downloaded 4366258 bytes via libgen — confirmation did not block the flow |
| S27 | remote | PASS | 2026-07-25 | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S28 | remote | PASS | 2026-07-25 | model used read outline=true; 140 table-of-contents entr(ies) returned |
| S29 | remote | PASS | 2026-07-25 | open-access discovery surfaced 35 hit(s); model referenced one in its answer |
| S30 | remote | PASS | 2026-07-25 | model set enrich=true; Crossref journal="Cell" citations=56401; model answered the ask |
| S31 | remote | PASS | 2026-07-25 | model searched, called get_details, and surfaced the returned BibTeX citation |
| S32 | local | PASS | 2026-07-25 | escalation surfaced 7 Anna's-origin result(s); model did not report not-found |
| S33 | remote | PASS | 2026-07-25 | escalation surfaced 7 Anna's-origin result(s); model did not report not-found |
| S34 | local | PASS | 2026-07-25 | model searched, found an Anna's-origin item, and downloaded it (md5=00dd2b0b58e81e3c6e7cb9e7b72dee23) |
| S35 | remote | PASS | 2026-07-25 | model searched, found an Anna's-origin item, and downloaded it (md5=00dd2b0b58e81e3c6e7cb9e7b72dee23) |
| S36 | local | PASS | 2026-07-25 | get_details fell back to Anna's for the escalated md5 (collection=zlib) |
| S37 | remote | PASS | 2026-07-25 | get_details fell back to Anna's for the escalated md5 (collection=zlib) |
| S38 | local | PASS | 2026-07-25 | never mode honored and the model reported the miss honestly |
| S39 | local | PASS | 2026-07-25 | always mode consulted the extras alongside a 27-result catalog page (annas=2, open access=33) |
| S40 | local | PASS | 2026-07-25 | read opened an Anna's-only item (1536 chars extracted) |
| S41 | local | PASS | 2026-07-25 | member download reported the account allowance (48 of 50 left) |
| S42 | local | PASS | 2026-07-25 | nothing exists by that name and the model said so, inventing no metadata |
| S43 | local | PASS | 2026-07-25 | restriction held; the model routed through the permitted source instead of the refused one |
| S44 | local | PASS | 2026-07-25 | model set page=2 and received page 2 with 25 results |
| S45 | local | PASS | 2026-07-25 | downloaded 91408 bytes via europepmc |
| S46 | local | PASS | 2026-07-25 | downloaded 2114465 bytes via biorxiv |
| S47 | local | PASS | 2026-07-25 | downloaded 374166 bytes via fatcat |
| S48 | local | PASS | 2026-07-25 | core is absent from the download source enum on a deployment with no CORE key, and the model asked for it nowhere; enum = unpaywall, europepmc, biorxiv, fatcat, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S49 | local | PASS | 2026-07-25 | downloaded DOI via europepmc, an open-access provider — the chain preferred a legal copy |
| S50 | local | PASS | 2026-07-25 | model discovered the isbn key unaided; downloaded 15684454 bytes via archive |
| S51 | local | PASS | 2026-07-25 | downloaded 1850890 bytes via oapen |
| S52 | local | PASS | 2026-07-25 | downloaded 1850890 bytes via oapen |
| S53 | local | PASS | 2026-07-25 | oapen refused an identifier OAPEN does not hold cleanly, and the model reported the miss instead of presenting a file |
| S54 | local | PASS | 2026-07-25 | downloaded 15684454 bytes via archive |
| S55 | local | PASS | 2026-07-25 | archive refused a lending-restricted book cleanly, and the model reported the miss instead of presenting a file |
| S56 | local | PASS | 2026-07-25 | gutenberg surfaced 4 hit(s) with a fetchable file URL, and the model handed the link over |
| S57 | local | PASS | 2026-07-25 | eric surfaced 7 hit(s) with a fetchable file URL, and the model handed the link over |
| S58 | local | PASS | 2026-07-25 | dblp contributed 5 record(s), each labeled a citation rather than free full text, and the model answered from the merged results |
| S59 | local | PASS | 2026-07-25 | pubmed contributed 7 record(s), each labeled a citation rather than free full text, and the model answered from the merged results |
| S60 | local | PASS | 2026-07-25 | the chain recorded the dead source as unavailable and acted on it on the next pass ("source in cooldown, skipping") |
| S61 | local | PASS | 2026-07-30 | downloaded 502941 bytes via rfc |
| S62 | local | PASS | 2026-07-30 | downloaded 966908 bytes via nist |
| S63 | local | PASS | 2026-07-30 | downloaded 502941 bytes via rfc |
| S64 | local | PASS | 2026-07-30 | read opened RFC 9110 as text (6002 chars extracted) |
| S65 | local | PASS | 2026-07-30 | the download source enum advertises rfc and nist; enum = unpaywall, europepmc, biorxiv, rfc, nist, fatcat, core, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S66 | local | PASS | 2026-07-30 | downloaded 339463 bytes via dagstuhl |
| S67 | local | PASS | 2026-07-30 | downloaded 786279 bytes via acl |
| S68 | local | PASS | 2026-07-30 | downloaded 173300 bytes via zenodo |
