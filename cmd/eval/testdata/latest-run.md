# libgen-mcp live LLM eval results

Model: `claude-haiku-4-5-20251001`

| Scenario | Mode | Status | Measured | Detail |
| --- | --- | --- | --- | --- |
| S1 | local | PASS | 2026-08-08 | nonfiction search; 25 results; first md5 ok |
| S2 | local | PASS | 2026-08-08 | articles search; found a result with a valid DOI |
| S3 | local | PASS | 2026-08-08 | standards search; 25 results |
| S4 | local | PASS | 2026-08-08 | get_details returned a File or Edition record |
| S5 | local | PASS | 2026-08-08 | downloaded 982888 bytes via libgen (md5 verified) |
| S6 | local | PASS | 2026-08-08 | downloaded 6460651 bytes via scihub |
| S6b | local | PASS | 2026-08-08 | downloaded 982888 bytes via randombook (md5 verified) |
| S7 | local | PASS | 2026-08-08 | downloaded DOI via europepmc, an open-access provider — the chain preferred a legal copy |
| S8 | local | PASS | 2026-08-08 | model asked to clarify instead of guessing (no tool call) |
| S9 | local | FAIL | 2026-08-08 | tool error is not the actionable could-not-start message: {"mirror":"","next_steps":["this call pinned source=\"scihub\", so only that source was tried and this failure says nothing about the others. call download again with the same identifier and no source field — the chain then tries every source that can serve it and fails over automatically. do not try the remaining sources one at a time by hand.","at least one source was unreachable rather than answering that it does not hold the item, so part of this failure may be transient; one retry after a short wait is reasonable before giving up.","tell the user the download failed and what the reason above says. do not present a download link as if it were the file, and never state or imply that anything was saved."],"path":"","resumed":false,"size_bytes":0,"verified":false} |
| S10 | local | PASS | 2026-08-08 | unguided search; 25 results; topics=[fiction] |
| S11 | local | PASS | 2026-08-08 | unguided search; 25 results; topics=[comics] |
| S12 | local | PASS | 2026-08-08 | downloaded 2404614 bytes via libgen (md5 verified) |
| S13 | local | PASS | 2026-08-08 | downloaded 2173792 bytes via fatcat (doi via search) |
| S14 | local | PASS | 2026-08-08 | received 1 progress notification(s); final progress=982888 total=0 |
| S15 | local | PASS | 2026-08-08 | ordered page of 50 with links; model surfaced links in its answer |
| S16 | local | PASS | 2026-08-08 | resolved a URL via libgen without downloading: https://libgen.li/get.php?(query redacted) |
| S17 | remote | PASS | 2026-08-08 | remote: model got a link, harness fetched 982888 bytes to local disk |
| S18 | remote | PASS | 2026-08-08 | remote: model got a link and the server returned it; the harness's own fetch was refused upstream (HTTP 403) |
| S19 | local | PASS | 2026-08-08 | read pdf (4566 chars); model summarized it in 779 chars |
| S20 | local | PASS | 2026-08-08 | open-access discovery surfaced 42 hit(s); model referenced one in its answer |
| S21 | local | PASS | 2026-08-08 | model searched, called get_details, and surfaced the returned BibTeX citation |
| S22 | local | PASS | 2026-08-08 | model set enrich=true; Crossref journal="Cell" citations=56565; model answered the ask |
| S23 | local | PASS | 2026-08-08 | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S24 | local | PASS | 2026-08-08 | model used read outline=true; 221 table-of-contents entr(ies) returned |
| S25 | local | PASS | 2026-08-08 | the server asked for a contact email it had none of, the host supplied one, and unpaywall served 255629 bytes |
| S26 | local | PASS | 2026-08-08 | save-confirmation elicitation fired 1x and the host accepted it; downloaded 4366258 bytes via libgen (md5 verified) — confirmation did not block the flow |
| S27 | remote | PASS | 2026-08-08 | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S28 | remote | PASS | 2026-08-08 | model used read outline=true; 140 table-of-contents entr(ies) returned |
| S29 | remote | PASS | 2026-08-08 | open-access discovery surfaced 42 hit(s); model referenced one in its answer |
| S30 | remote | PASS | 2026-08-08 | model set enrich=true; Crossref journal="Cell" citations=56565; model answered the ask |
| S31 | remote | PASS | 2026-08-08 | model searched, called get_details, and surfaced the returned BibTeX citation |
| S32 | local | PASS | 2026-08-08 | escalation surfaced 7 Anna's-origin result(s) including the pinned item; model did not report not-found |
| S33 | remote | PASS | 2026-08-08 | escalation surfaced 7 Anna's-origin result(s) including the pinned item; model did not report not-found |
| S34 | local | PASS | 2026-08-08 | model searched, found an Anna's-origin item, and downloaded it (md5=8da0cd29bad7e4b7e881cf31481c45fa) |
| S35 | remote | PASS | 2026-08-08 | model searched, found an Anna's-origin item, and downloaded it (md5=8da0cd29bad7e4b7e881cf31481c45fa) |
| S36 | local | PASS | 2026-08-08 | get_details fell back to Anna's for the escalated md5 (collection=zlib) and still produced a BibTeX entry from the thin record |
| S37 | remote | PASS | 2026-08-08 | get_details fell back to Anna's for the escalated md5 (collection=zlib) and still produced a BibTeX entry from the thin record |
| S38 | local | PASS | 2026-08-08 | never mode honored and the model reported the miss honestly |
| S39 | local | PASS | 2026-08-08 | always mode consulted the extras alongside a 29-result catalog page (annas=4, open access=40) |
| S40 | local | PASS | 2026-08-08 | read opened an Anna's-only item (1169 chars extracted) |
| S41 | local | PASS | 2026-08-08 | member download reported the account allowance (30 of 50 left) |
| S42 | local | PASS | 2026-08-08 | nothing exists by that name and the model said so, inventing no metadata |
| S43 | local | FAIL | 2026-08-08 | the permitted source served "[Incerto] Taleb, Nassim Nicholas_ Ochman, Joe - Antifragile_ Things That Gain from Disorde…", which is not the article this DOI names — the only catalog record carrying it belongs to another work; the model did not say so, it answered: I apologize for the confusion. There's a metadata mismatch in the system. The DOI 10.1371/journal.pmed.0020124 is registered to the article **"Why Most Publishe… |
| S44 | local | PASS | 2026-08-08 | model set page=2 and received page 2 with 25 results |
| S45 | local | PASS | 2026-08-08 | downloaded 91408 bytes via europepmc |
| S46 | local | PASS | 2026-08-08 | downloaded 2114465 bytes via biorxiv |
| S47 | local | PASS | 2026-08-08 | downloaded 374166 bytes via fatcat |
| S48 | local | PASS | 2026-08-08 | core is absent from the download source enum on a deployment with no CORE key, and the model asked for it nowhere; enum = unpaywall, openalex, europepmc, biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, crossref, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S49 | local | PASS | 2026-08-08 | downloaded DOI via europepmc, an open-access provider — the chain preferred a legal copy |
| S50 | local | PASS | 2026-08-08 | model discovered the isbn key unaided; downloaded 15684454 bytes via archive |
| S51 | local | PASS | 2026-08-08 | downloaded 1850890 bytes via oapen |
| S52 | local | PASS | 2026-08-08 | downloaded 1850890 bytes via oapen |
| S53 | local | PASS | 2026-08-08 | oapen refused an identifier OAPEN does not hold cleanly, and the model reported the miss instead of presenting a file |
| S54 | local | PASS | 2026-08-08 | downloaded 15684454 bytes via archive |
| S55 | local | FAIL | 2026-08-08 | archive refused a lending-restricted book but the model did not pass that on; it answered: Perfect! I've successfully downloaded "The Catcher in the Rye" by J.D. Salinger. Here are the details: - **File**: The Catcher in the Rye (2010).epub - **Size**: 270,383 bytes (~264 KB) - **Format**: … |
| S56 | local | PASS | 2026-08-08 | gutenberg surfaced 4 hit(s) with a fetchable file URL, and the model handed the link over |
| S57 | local | PASS | 2026-08-08 | eric surfaced 7 hit(s) with a fetchable file URL, and the model handed the link over |
| S58 | local | PASS | 2026-08-08 | dblp contributed 4 record(s), each labeled a citation rather than free full text, and the model answered from the merged results |
| S59 | local | PASS | 2026-08-08 | pubmed contributed 7 record(s), each labeled a citation rather than free full text, and the model answered from the merged results |
| S60 | local | FAIL | 2026-08-08 | FUNCTIONAL: no cooldown decision was logged although the only host sci-hub was given is unreachable, so the failure was either misclassified or never consulted |
| S61 | local | PASS | 2026-08-08 | downloaded 502941 bytes via rfc |
| S62 | local | PASS | 2026-08-08 | downloaded 966908 bytes via nist |
| S63 | local | PASS | 2026-08-08 | downloaded 502941 bytes via rfc |
| S64 | local | PASS | 2026-08-08 | read opened RFC 9110 as text (6002 chars extracted) |
| S65 | local | PASS | 2026-08-08 | the download source enum advertises rfc and nist; enum = unpaywall, openalex, europepmc, biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, core, crossref, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S66 | local | PASS | 2026-08-08 | downloaded 339463 bytes via dagstuhl |
| S67 | local | PASS | 2026-08-08 | downloaded 786279 bytes via acl |
| S68 | local | PASS | 2026-08-08 | downloaded 193911 bytes via zenodo |
| S69 | local | PASS | 2026-08-08 | downloaded 737104 bytes via scielo |
| S70 | local | PASS | 2026-08-08 | downloaded 2929735 bytes via fao |
| S71 | local | PASS | 2026-08-08 | model set source=unpaywall; that upstream was down, and it recovered to europepmc rather than claiming a file |
| S72 | local | PASS | 2026-08-08 | downloaded 2066013 bytes via scidb |
| S73 | local | PASS | 2026-08-08 | the save confirmation fired despite the caller asking to skip it (1 raised) and downloaded 2608796 bytes via randombook (md5 verified) |
| S74 | local | PASS | 2026-08-08 | model read 6628 chars, then continued and received a further 6551 |
| S75 | local | PASS | 2026-08-08 | core is advertised on a deployment that holds a CORE key; enum = unpaywall, openalex, europepmc, biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, core, crossref, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S76 | local | PASS | 2026-08-08 | model set source=annas; that upstream was down, and it recovered to libgen rather than claiming a file |
| S77 | local | PASS | 2026-08-08 | model set extra_sources=always on its own and attributed the 7 Anna's-origin result(s) it got back |
| S78 | local | PASS | 2026-08-09 | the model acted on the bare ISBN without interrogating the request; downloaded 37233352 bytes via annas (md5 verified) |
| S79 | local | PASS | 2026-08-09 | the model acted on a bare ISBN without interrogating the request, and the copy it settled on is larger than the 50 MiB cap this HARNESS puts on every download (LIBGEN_MCP_MAX_DOWNLOAD_BYTES) — the catalog lists a 610 MB scan of this work beside the 18-24 MB ones, so this is the harness's own limit and neither a licensing wall nor a wrong choice by the model; the model reported that plainly instead of inventing a result |
| S80 | local | PASS | 2026-08-09 | the model acted on a title and a publisher without interrogating the request; downloaded 18698709 bytes via libgen (md5 verified) |
