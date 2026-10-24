# Shared scalar default design review r2

Source CLI944bac64fd58bf450bdc723d1cb1c944f89a47ff, corev0.77.0. Explicitly withdraw r1 major as FALSE POSITIVE: I inspected ParameterDef in isolation and missed actual enclosing types' JSON codecs. comparison_mapping_codec.go166-193 defines BOTH QueryDef and QueryDefWithFolderPath UnmarshalJSON with decoder.UseNumber. MCP loadQuery decodes QueryDefWithFolderPath, so client.request json.Unmarshal invokes that lossless custom decoder. Filestore store_queries_revisioned.go57-58 likewise unmarshals actual QueryDef and retains same number behavior. No transport/parser change is necessary or justified by r1.

Original shared narrow scalar default converter design passes without amendment. Preserve generic driverrow fromGoValue; share declared-default conversion between MCP missingdefault filling and authoritative server defaultorigin equality. Manual supplied values win; omitted valid defaults get OriginDefault, subject to fresh definition/authority. Keep declared scalar whitelist, ParameterDef.Validate plus exact TypedValue.Validate, credential/multivalue rejection, core safeintegral float restriction, no floatdecimal precision invention, finite number and canonicaldate/UTCdatetime checks. nil remains absent default.

r1 testmatrix remains useful validation, not evidence of a current parser defect: actual file/queryHTTP decode preserving json.Number; missing5→manual7→clear5 and origin evidence; false/empty; exactdecimal/int64bounds/safeinteger floats/unsafe/nonfinite/invaliddefaults; forgeddefaultorigin rejected; unchanged driverrow behavior and changeddefault handle revocation. Author should exercise actual types rather than reproducing reviewer's mistaken ParameterDef-only assumption.

No runtime test or source edit performed in this correction. Source evidence resolves finding; no new parser scope requested. No open findings, no declined finding (r1 withdrawn). Root sole landing owner.

VERDICT: blockers=0 majors=0 minors=0 land=yes
