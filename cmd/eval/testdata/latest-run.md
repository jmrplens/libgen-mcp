# libgen-mcp live LLM eval results

Model: `claude-haiku-4-5-20251001`

| Scenario | Mode | Status | Measured | Detail |
| --- | --- | --- | --- | --- |
| S1 | local | PASS | 2026-09-22 | nonfiction search; 25 results; first md5 ok |
| S2 | local | PASS | 2026-09-22 | articles search; found a result with a valid DOI |
| S3 | local | PASS | 2026-09-22 | standards search; 25 results |
| S4 | local | PASS | 2026-09-22 | get_details returned a File or Edition record |
| S5 | local | PASS | 2026-09-22 | downloaded 982888 bytes via libgen (md5 verified) |
| S6 | local | PASS | 2026-09-22 | downloaded 6460651 bytes via scihub |
| S6b | local | PASS | 2026-09-22 | downloaded 982888 bytes via randombook (md5 verified) |
| S7 | local | PASS | 2026-09-22 | downloaded DOI via fatcat, an open-access provider — the chain preferred a legal copy |
| S8 | local | PASS | 2026-09-22 | model asked to clarify instead of guessing (no tool call) |
| S9 | local | PASS | 2026-09-22 | start-retries exhausted; actionable error surfaced and the model did not fabricate success |
| S10 | local | PASS | 2026-09-22 | unguided search; 25 results; topics=[fiction] |
| S11 | local | PASS | 2026-09-22 | unguided search; 25 results; topics=[comics] |
| S12 | local | PASS | 2026-09-22 | downloaded 2404614 bytes via randombook (md5 verified) |
| S13 | local | PASS | 2026-09-22 | downloaded 2173792 bytes via fatcat (doi via search) |
| S14 | local | PASS | 2026-09-22 | received 16 progress notification(s); final progress=982888 total=982888 |
| S15 | local | PASS | 2026-09-22 | ordered page of 100 with links; model surfaced links in its answer |
| S16 | local | PASS | 2026-09-22 | resolved a URL via libgen without downloading: https://libgen.li/get.php?(query redacted) |
| S17 | remote | PASS | 2026-09-22 | remote: model got a link, harness fetched 638 bytes to local disk |
| S18 | remote | PASS | 2026-09-22 | remote: the server returned a fetchable link, which is the whole of what this grades — the harness's own fetch of that link was then refused by the third party hosting the file (HTTP 403), which is the host's decision about the harness, not a failure of the remote contract |
| S19 | local | PASS | 2026-09-22 | read pdf (4566 chars); model summarized it in 783 chars |
| S20 | local | PASS | 2026-09-22 | open-access discovery surfaced 33 hit(s); model referenced one in its answer |
| S21 | local | PASS | 2026-09-22 | model searched, called get_details, and surfaced the returned BibTeX citation |
| S22 | local | PASS | 2026-09-22 | model set enrich=true; Crossref journal="Cell" citations=57491; model answered the ask |
| S23 | local | PASS | 2026-09-22 | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S24 | local | PASS | 2026-09-22 | model used read outline=true; 205 table-of-contents entr(ies) returned |
| S25 | local | PASS | 2026-09-22 | the server asked for a contact email it had none of, the host supplied one, and unpaywall served 255629 bytes |
| S26 | local | PASS | 2026-09-22 | save-confirmation elicitation fired 1x and the host accepted it; downloaded 4366258 bytes via libgen (md5 verified) — confirmation did not block the flow |
| S27 | remote | PASS | 2026-09-22 | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S28 | remote | PASS | 2026-09-22 | no embedded table of contents; the model read the document and compiled one from its text |
| S29 | remote | PASS | 2026-09-22 | open-access discovery surfaced 35 hit(s); model referenced one in its answer |
| S30 | remote | PASS | 2026-09-22 | model set enrich=true; Crossref journal="Cell" citations=57491; model answered the ask |
| S31 | remote | PASS | 2026-09-22 | model searched, called get_details, and surfaced the returned BibTeX citation |
| S32 | local | PASS | 2026-09-22 | only open-access hits, no Anna's-origin results today; the model reported that plainly instead of inventing a result |
| S33 | remote | PASS | 2026-09-22 | only open-access hits, no Anna's-origin results today; the model reported that plainly instead of inventing a result |
| S34 | local | PASS | 2026-09-22 | no Anna's-origin result to download (live network); the model reported that plainly instead of inventing a result |
| S35 | remote | PASS | 2026-09-22 | no Anna's-origin result to download (live network); the model reported that plainly instead of inventing a result |
| S36 | local | PASS | 2026-09-22 | only open-access hits, no Anna's-origin results today; the model reported that plainly instead of inventing a result |
| S37 | remote | PASS | 2026-09-22 | only open-access hits, no Anna's-origin results today; the model reported that plainly instead of inventing a result |
| S38 | local | PASS | 2026-09-22 | never mode honored and the model reported the miss honestly |
| S39 | local | SKIP | 2026-09-22 | SKIP: always mode reached the open-access providers (33 hit(s)) but Anna's returned nothing, so the shadow-library half of the forced escalation went ungraded |
| S40 | local | PASS | 2026-09-22 | only open-access hits, no Anna's-origin results today; the model reported that plainly instead of inventing a result |
| S41 | local | PASS | 2026-09-22 | no Anna's-origin result to download (live network); the model reported that plainly instead of inventing a result |
| S42 | local | PASS | 2026-09-22 | nothing exists by that name and the model said so, inventing no metadata |
| S43 | local | PASS | 2026-09-22 | restriction held and the model reported the refusal instead of claiming a file |
| S44 | local | PASS | 2026-09-22 | model set page=2 and received page 2 with 25 results |
| S45 | local | PASS | 2026-09-22 | downloaded 91408 bytes via europepmc |
| S46 | local | PASS | 2026-09-22 | downloaded 2114465 bytes via biorxiv |
| S47 | local | PASS | 2026-09-22 | downloaded 374166 bytes via fatcat |
| S48 | local | PASS | 2026-09-22 | core is absent from the download source enum on a deployment with no CORE key, and the model asked for it nowhere; enum = unpaywall, openalex, europepmc, biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, crossref, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S49 | local | PASS | 2026-09-22 | downloaded DOI via fatcat, an open-access provider — the chain preferred a legal copy |
| S50 | local | PASS | 2026-09-22 | model discovered the isbn key unaided; downloaded 15684454 bytes via archive |
| S51 | local | PASS | 2026-09-22 | downloaded 1850890 bytes via oapen |
| S52 | local | PASS | 2026-09-22 | downloaded 1850890 bytes via oapen |
| S53 | local | PASS | 2026-09-22 | oapen refused an identifier OAPEN does not hold cleanly, and the model reported the miss instead of presenting a file |
| S54 | local | PASS | 2026-09-22 | downloaded 15684454 bytes via archive |
| S55 | local | PASS | 2026-09-22 | archive refused a lending-restricted book cleanly, and the model reported the miss instead of presenting a file |
| S56 | local | SKIP | 2026-09-22 | SKIP: gutenberg contributed nothing to this federated search of 34 hit(s), so there is none of its output to grade |
| S57 | local | PASS | 2026-09-22 | eric surfaced 7 hit(s) with a fetchable file URL, and the model handed the link over |
| S58 | local | SKIP | 2026-09-22 | SKIP: dblp contributed nothing to this federated search of 21 hit(s), so there is none of its output to grade |
| S59 | local | PASS | 2026-09-22 | pubmed contributed 7 record(s), each labeled a citation rather than free full text, and the model answered from the merged results |
| S60 | local | PASS | 2026-09-22 | the chain recorded the dead source as unavailable and acted on it on a later call ("source in cooldown, skipping") |
| S61 | local | PASS | 2026-09-22 | downloaded 502941 bytes via rfc |
| S62 | local | PASS | 2026-09-22 | downloaded 966908 bytes via nist |
| S63 | local | PASS | 2026-09-22 | downloaded 502941 bytes via rfc |
| S64 | local | PASS | 2026-09-22 | read opened RFC 9110 as text (2002 chars extracted) |
| S65 | local | PASS | 2026-09-22 | the download source enum advertises rfc and nist; enum = unpaywall, openalex, europepmc, biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, core, crossref, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S66 | local | PASS | 2026-09-22 | downloaded 339463 bytes via dagstuhl |
| S67 | local | PASS | 2026-09-22 | downloaded 786279 bytes via acl |
| S68 | local | PASS | 2026-09-22 | downloaded 193911 bytes via zenodo |
| S69 | local | PASS | 2026-09-22 | downloaded 737104 bytes via scielo |
| S70 | local | PASS | 2026-09-22 | downloaded 2929735 bytes via fao |
| S71 | local | PASS | 2026-09-22 | model set source=unpaywall; that source could not serve the item and the pinned call failed — a pin is the whole chain, so nothing was substituted behind it. The model then called download again without pinning a source, and fatcat served that call: it routed around the dead source itself rather than claiming a file |
| S72 | local | PASS | 2026-09-22 | model set source=scidb correctly but the live download failed (mirror/network); the model reported that plainly instead of inventing a result |
| S73 | local | PASS | 2026-09-22 | the save confirmation fired despite the caller asking to skip it (1 raised) and downloaded 2608796 bytes via libgen (md5 verified) |
| S74 | local | PASS | 2026-09-22 | model read 6628 chars, then continued and received a further 6551 |
| S75 | local | PASS | 2026-09-22 | core is advertised on a deployment that holds a CORE key; enum = unpaywall, openalex, europepmc, biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, core, crossref, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S76 | local | PASS | 2026-09-22 | model set source=annas; that source could not serve the item and the pinned call failed — a pin is the whole chain, so nothing was substituted behind it. The model then called download again without pinning a source, and libgen served that call: it routed around the dead source itself rather than claiming a file |
| S77 | local | PASS | 2026-09-22 | model widened the search itself but no Anna's-origin result came back today (21 open-access hit(s), live network); the model reported that plainly instead of inventing a result |
| S78 | local | PASS | 2026-09-22 | the model went straight to download on a bare ISBN, but the live fetch failed (an in-copyright title has no open-access ISBN route); the model reported that plainly instead of inventing a result |
| S79 | local | PASS | 2026-09-22 | the model acted on a title and a publisher without interrogating the request; downloaded 18698709 bytes via randombook (md5 verified) |
| S80 | local | PASS | 2026-09-22 | the model searched the topic and downloaded a result it chose itself, without interrogating the request; downloaded 2469221 bytes via randombook (md5 verified) |
