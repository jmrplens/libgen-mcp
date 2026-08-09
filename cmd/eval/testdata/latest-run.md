# libgen-mcp live LLM eval results

Model: `claude-haiku-4-5-20251001`

| Scenario | Mode | Status | Measured | Detail |
| --- | --- | --- | --- | --- |
| S1 | local | PASS | 2026-08-09 | nonfiction search; 25 results; first md5 ok |
| S2 | local | PASS | 2026-08-09 | articles search; found a result with a valid DOI |
| S3 | local | PASS | 2026-08-09 | standards search; 25 results |
| S4 | local | PASS | 2026-08-09 | get_details returned a File or Edition record |
| S5 | local | PASS | 2026-08-09 | downloaded 982888 bytes via randombook (md5 verified) |
| S6 | local | PASS | 2026-08-09 | downloaded 6460651 bytes via scihub |
| S6b | local | PASS | 2026-08-09 | downloaded 982888 bytes via randombook (md5 verified) |
| S7 | local | PASS | 2026-08-09 | downloaded DOI via europepmc, an open-access provider — the chain preferred a legal copy |
| S8 | local | PASS | 2026-08-09 | model asked to clarify instead of guessing (no tool call) |
| S9 | local | PASS | 2026-08-09 | start-retries exhausted; actionable error surfaced and the model did not fabricate success |
| S10 | local | PASS | 2026-08-09 | unguided search; 25 results; topics=[fiction] |
| S11 | local | PASS | 2026-08-09 | unguided search; 25 results; topics=[comics] |
| S12 | local | PASS | 2026-08-09 | downloaded 2404614 bytes via randombook (md5 verified) |
| S13 | local | PASS | 2026-08-09 | downloaded 2173792 bytes via fatcat (doi via search) |
| S14 | local | PASS | 2026-08-09 | received 1 progress notification(s); final progress=982888 total=0 |
| S15 | local | PASS | 2026-08-09 | ordered page of 50 with links; model surfaced links in its answer |
| S16 | local | PASS | 2026-08-09 | resolved a URL via libgen without downloading: https://libgen.li/get.php?(query redacted) |
| S17 | remote | PASS | 2026-08-09 | remote: the server returned a fetchable link, which is the whole of what this grades — the harness's own fetch of that link was then refused by the third party hosting the file (HTTP 500), which is the host's decision about the harness, not a failure of the remote contract |
| S18 | remote | PASS | 2026-08-09 | remote: the server returned a fetchable link, which is the whole of what this grades — the harness's own fetch of that link was then refused by the third party hosting the file (Get "/article/S0092867411001279/pdf": stopped after 10 redirects), which is the host's decision about the harness, not a failure of the remote contract |
| S19 | local | PASS | 2026-08-09 | read pdf (4566 chars); model summarized it in 882 chars |
| S20 | local | PASS | 2026-08-09 | open-access discovery surfaced 35 hit(s); model referenced one in its answer |
| S21 | local | PASS | 2026-08-09 | model searched, called get_details, and surfaced the returned BibTeX citation |
| S22 | local | PASS | 2026-08-09 | model set enrich=true; Crossref journal="Cell" citations=56572; model answered the ask |
| S23 | local | PASS | 2026-08-09 | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S24 | local | PASS | 2026-08-09 | model used read outline=true; 221 table-of-contents entr(ies) returned |
| S25 | local | PASS | 2026-08-09 | the server asked for a contact email it had none of, the host supplied one, and unpaywall served 255629 bytes |
| S26 | local | PASS | 2026-08-09 | save-confirmation elicitation fired 1x and the host accepted it; downloaded 4366258 bytes via libgen (md5 verified) — confirmation did not block the flow |
| S27 | remote | PASS | 2026-08-09 | model used read find="pointer"; 503 match(es); model surfaced a passage |
| S28 | remote | PASS | 2026-08-09 | model used read outline=true; 140 table-of-contents entr(ies) returned |
| S29 | remote | PASS | 2026-08-09 | open-access discovery surfaced 28 hit(s); model referenced one in its answer |
| S30 | remote | PASS | 2026-08-09 | model set enrich=true; Crossref journal="Cell" citations=56572; model answered the ask |
| S31 | remote | PASS | 2026-08-09 | model searched, called get_details, and surfaced the returned BibTeX citation |
| S32 | local | PASS | 2026-08-09 | escalation surfaced 7 Anna's-origin result(s) including the pinned item; model did not report not-found |
| S33 | remote | PASS | 2026-08-09 | escalation surfaced 7 Anna's-origin result(s) including the pinned item; model did not report not-found |
| S34 | local | PASS | 2026-08-09 | model searched, found an Anna's-origin item, and downloaded it (md5=8da0cd29bad7e4b7e881cf31481c45fa) |
| S35 | remote | PASS | 2026-08-09 | model searched, found an Anna's-origin item, and downloaded it (md5=8da0cd29bad7e4b7e881cf31481c45fa) |
| S36 | local | PASS | 2026-08-09 | get_details fell back to Anna's for the escalated md5 (collection=zlib) and still produced a BibTeX entry from the thin record |
| S37 | remote | PASS | 2026-08-09 | get_details fell back to Anna's for the escalated md5 (collection=zlib) and still produced a BibTeX entry from the thin record |
| S38 | local | PASS | 2026-08-09 | never mode honored and the model reported the miss honestly |
| S39 | local | PASS | 2026-08-09 | always mode consulted the extras alongside a 29-result catalog page (annas=4, open access=40) |
| S40 | local | PASS | 2026-08-09 | read opened an Anna's-only item (1169 chars extracted) |
| S41 | local | PASS | 2026-08-09 | member download reported the account allowance (24 of 50 left) |
| S42 | local | PASS | 2026-08-09 | nothing exists by that name and the model said so, inventing no metadata |
| S43 | local | PASS | 2026-08-09 | the restriction held — the DOI download was refused, as a catalog-only deployment must refuse it, and nothing outside the permitted list served anything. The model then routed through the permitted source, which served "Taleb, Nassim Nicholas et al. - Antifragile - Things That Gain from Disorder (2012).epub": not the article this DOI names, because the only catalog record carrying that DOI carries it by mistake and belongs to another work. The mis-keyed record is the catalog's error, so what is graded from here is the answer; the model reported the miss plainly instead of inventing a result |
| S44 | local | PASS | 2026-08-09 | model set page=2 and received page 2 with 25 results |
| S45 | local | PASS | 2026-08-09 | downloaded 91408 bytes via europepmc |
| S46 | local | PASS | 2026-08-09 | downloaded 2114465 bytes via biorxiv |
| S47 | local | PASS | 2026-08-09 | downloaded 374166 bytes via fatcat |
| S48 | local | PASS | 2026-08-09 | core is absent from the download source enum on a deployment with no CORE key, and the model asked for it nowhere; enum = unpaywall, openalex, europepmc, biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, crossref, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S49 | local | PASS | 2026-08-09 | downloaded DOI via europepmc, an open-access provider — the chain preferred a legal copy |
| S50 | local | PASS | 2026-08-09 | model discovered the isbn key unaided; downloaded 15684454 bytes via archive |
| S51 | local | PASS | 2026-08-09 | downloaded 1850890 bytes via oapen |
| S52 | local | PASS | 2026-08-09 | downloaded 1850890 bytes via oapen |
| S53 | local | PASS | 2026-08-09 | oapen refused an identifier OAPEN does not hold cleanly, and the model reported the miss instead of presenting a file |
| S54 | local | PASS | 2026-08-09 | downloaded 15684454 bytes via archive |
| S55 | local | PASS | 2026-08-09 | archive refused a lending-restricted book cleanly, and the model reported the miss instead of presenting a file |
| S56 | local | PASS | 2026-08-09 | gutenberg surfaced 4 hit(s) with a fetchable file URL, and the model handed the link over |
| S57 | local | PASS | 2026-08-09 | eric surfaced 7 hit(s) with a fetchable file URL, and the model handed the link over |
| S58 | local | PASS | 2026-08-09 | dblp contributed 4 record(s), each labeled a citation rather than free full text, and the model answered from the merged results |
| S59 | local | PASS | 2026-08-09 | pubmed contributed 7 record(s), each labeled a citation rather than free full text, and the model answered from the merged results |
| S60 | local | PASS | 2026-08-09 | the chain recorded the dead source as unavailable and acted on it on a later call ("source in cooldown, skipping") |
| S61 | local | PASS | 2026-08-09 | downloaded 502941 bytes via rfc |
| S62 | local | PASS | 2026-08-09 | downloaded 966908 bytes via nist |
| S63 | local | PASS | 2026-08-09 | downloaded 502941 bytes via rfc |
| S64 | local | PASS | 2026-08-09 | read opened RFC 9110 as text (6002 chars extracted) |
| S65 | local | PASS | 2026-08-09 | the download source enum advertises rfc and nist; enum = unpaywall, openalex, europepmc, biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, core, crossref, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S66 | local | PASS | 2026-08-09 | downloaded 339463 bytes via dagstuhl |
| S67 | local | PASS | 2026-08-09 | downloaded 786279 bytes via acl |
| S68 | local | PASS | 2026-08-09 | downloaded 193911 bytes via zenodo |
| S69 | local | PASS | 2026-08-09 | downloaded 737104 bytes via scielo |
| S70 | local | PASS | 2026-08-09 | downloaded 2929735 bytes via fao |
| S71 | local | PASS | 2026-08-09 | model set source=unpaywall; that source could not serve the item and the pinned call failed — a pin is the whole chain, so nothing was substituted behind it. The model then called download again without pinning a source, and europepmc served that call: it routed around the dead source itself rather than claiming a file |
| S72 | local | PASS | 2026-08-09 | downloaded 2066013 bytes via scidb |
| S73 | local | PASS | 2026-08-09 | the save confirmation fired despite the caller asking to skip it (1 raised) and downloaded 2608796 bytes via randombook (md5 verified) |
| S74 | local | PASS | 2026-08-09 | model read 6628 chars, then continued and received a further 6551 |
| S75 | local | PASS | 2026-08-09 | core is advertised on a deployment that holds a CORE key; enum = unpaywall, openalex, europepmc, biorxiv, rfc, nist, dagstuhl, acl, zenodo, scielo, fao, fatcat, core, crossref, oapen, archive, scihub, scidb, libgen, randombook, annas |
| S76 | local | PASS | 2026-08-09 | model set source=annas; that source could not serve the item and the pinned call failed — a pin is the whole chain, so nothing was substituted behind it. The model then called download again without pinning a source, and randombook served that call: it routed around the dead source itself rather than claiming a file |
| S77 | local | PASS | 2026-08-09 | model set extra_sources=always on its own and attributed the 7 Anna's-origin result(s) it got back |
| S78 | local | PASS | 2026-08-09 | the model acted on the bare ISBN without interrogating the request; downloaded 37233352 bytes via annas (md5 verified) |
| S79 | local | PASS | 2026-08-09 | the model acted on a title and a publisher without interrogating the request; downloaded 18698709 bytes via randombook (md5 verified) |
| S80 | local | PASS | 2026-08-09 | the model searched the topic and downloaded a result it chose itself, without interrogating the request; downloaded 13864769 bytes via randombook (md5 verified) |
