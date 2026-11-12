import assert from "node:assert/strict";
import { execFileSync, spawnSync } from "node:child_process";
import { createHash } from "node:crypto";
import { mkdir, mkdtemp, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");
const git = (...args) => execFileSync("git", args, { cwd: repository, encoding: "utf8" }).trim();
const commit = git("rev-parse", "HEAD");
const dirty = git("status", "--porcelain") !== "";
console.log(`Browser qualification: ${commit} (${dirty ? "dirty" : "clean"} tree)`);
// CI-only failing-head evidence carrier; the normal browser cases follow unchanged.
let carrierError;
if (process.env.CI) {
  const source = "737c2a273ca8d14d3cf0953a047dd3fb1eb8faf5";
  const sourceTree = "defe403e8f54ef308f3c8897ad70016436c2bb36";
  const inputTree = "32baf512679317c443e8a0f70178a1bce056c8ee";
  const patchSHA256 = "ffd8d99edeb923df1d1a856675e3c9e91a37c5d224a4d5b572dbfcb39c9dbd1e";
  const patchBase64 =
    "ZGlmZiAtLWdpdCBhL2ludGVybmFsL3Byb2Nlc3NpbmcvZG9jdW1lbnRfZXZlbnRzX2VtYWlsX3pvbmVfdGVzdC5nbyBiL2ludGVybmFsL3Byb2Nlc3NpbmcvZG9jdW1lbnRfZXZlbnRzX2VtYWlsX3pvbmVfdGVzdC5nbwppbmRleCBjYjFjYWE5MWVhN2NiYWQ3ODAzODJiOTExZmVhNGUxMGQ2NjNlMTNlLi5jMjk3NTVjODU1Mzk3NzQ4ZGU3YWQ2YTczZjNmMzZhYzlhMjA2MzVmIDEwMDY0NAotLS0gYS9pbnRlcm5hbC9wcm9jZXNzaW5nL2RvY3VtZW50X2V2ZW50c19lbWFpbF96b25lX3Rlc3QuZ28KKysrIGIvaW50ZXJuYWwvcHJvY2Vzc2luZy9kb2N1bWVudF9ldmVudHNfZW1haWxfem9uZV90ZXN0LmdvCkBAIC0xLDggKzEsMTAgQEAKIHBhY2thZ2UgcHJvY2Vzc2luZwogCiBpbXBvcnQgKAotCSJnby5rZW5uLmlvL2RvY2JhbmsvZG9jdW1lbnQiCiAJInRlc3RpbmciCisKKwkiZ2l0aHViLmNvbS9zdHJldGNoci90ZXN0aWZ5L3JlcXVpcmUiCisJImdvLmtlbm4uaW8vZG9jYmFuay9kb2N1bWVudCIKICkKIAogZnVuYyBUZXN0RW1haWxEYXRlWm9uZXModCAqdGVzdGluZy5UKSB7CkBAIC00MSwzICs0Myw3NiBAQCBmdW5jIFRlc3RJbnZhbGlkRW1haWxPZmZzZXRSZXRhaW5lZFRocm91Z2hNZXRhZGF0YSh0ICp0ZXN0aW5nLlQpIHsKIAkJdC5GYXRhbGYoImZpZWxkcyA9ICUrdiIsIHJlY29yZC5GaWVsZHMpCiAJfQogfQorCisvLyBUZXN0RW1haWxEYXRlRXh0cmFjdGlvblRvRXZlbnQgZXhlcmNpc2VzIHRoZSBzaGFyZWQgZGVjb2RlciBhbmQgbWV0YWRhdGEKKy8vIGV4dHJhY3RvciBiZWZvcmUgZXZlbnQgZGVyaXZhdGlvbiwgaW5jbHVkaW5nIGlucHV0cyB0aGUgZGVjb2RlciBjYW5ub3Qgbm9ybWFsaXplLgorZnVuYyBUZXN0RW1haWxEYXRlRXh0cmFjdGlvblRvRXZlbnQodCAqdGVzdGluZy5UKSB7CisJdC5QYXJhbGxlbCgpCisJZm9yIF8sIHRjIDo9IHJhbmdlIFtdc3RydWN0IHsKKwkJbmFtZSwgaGVhZGVycywgcmF3LCB2YWx1ZSBzdHJpbmcKKwkJcHJlY2lzaW9uIGRvY3VtZW50LkV2ZW50UHJlY2lzaW9uCisJCXpvbmUgZG9jdW1lbnQuRXZlbnRUaW1lem9uZUtpbmQKKwkJem9uZVRleHQsIHV0YyBzdHJpbmcKKwkJaGFzT2Zmc2V0IGJvb2wKKwkJb2Zmc2V0IGludAorCQl3YW50U2VudCBib29sCisJfXsKKwkJe25hbWU6ICJ3ZWVrZGF5X21pc21hdGNoIiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMDkgU2VwIDIwMjYgMTI6MzQ6NTYgKzAwMDBcclxuIn0sCisJCXtuYW1lOiAidW5rbm93bl93ZWVrZGF5IiwgaGVhZGVyczogIkRhdGU6IEZyeSwgMiBKYW4gMjAyNCAwMzowNCArMDAwMFxyXG4ifSwKKwkJe25hbWU6ICJpbnZhbGlkX2NhbGVuZGFyIiwgaGVhZGVyczogIkRhdGU6IDMwIEZlYiAyMDI0IDAzOjA0ICswMDAwXHJcbiJ9LAorCQl7bmFtZTogImFtYmlndW91c19kdXBsaWNhdGUiLCBoZWFkZXJzOiAiRGF0ZTogVHVlLCAyIEphbiAyMDI0IDAzOjA0ICswMDAwXHJcbkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwNDowNCArMDAwMFxyXG4ifSwKKwkJe25hbWU6ICJ2YWxpZF93ZWVrZGF5X2NvbXBhY3RfdXRjIiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCArMDAwMFxyXG4iLCByYXc6ICJUdWUsIDIgSmFuIDIwMjQgMDM6MDQgKzAwMDAiLCB2YWx1ZTogIjIwMjQtMDEtMDJUMDM6MDQiLCBwcmVjaXNpb246ICJtaW51dGUiLCB6b25lOiAidXRjIiwgem9uZVRleHQ6ICIrMDAwMCIsIHV0YzogIjIwMjQtMDEtMDJUMDM6MDQ6MDAuMDAwMDAwMDAwWiIsIHdhbnRTZW50OiB0cnVlfSwKKwkJe25hbWU6ICJjb2xvbl9wb3NpdGl2ZV96ZXJvIiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCArMDA6MDBcclxuIiwgcmF3OiAiVHVlLCAyIEphbiAyMDI0IDAzOjA0ICswMDowMCIsIHZhbHVlOiAiMjAyNC0wMS0wMlQwMzowNCIsIHByZWNpc2lvbjogIm1pbnV0ZSIsIHpvbmU6ICJ1dGMiLCB6b25lVGV4dDogIiswMDowMCIsIHV0YzogIjIwMjQtMDEtMDJUMDM6MDQ6MDAuMDAwMDAwMDAwWiIsIHdhbnRTZW50OiB0cnVlfSwKKwkJe25hbWU6ICJjb2xvbl9uZWdhdGl2ZV96ZXJvIiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCAtMDA6MDBcclxuIiwgcmF3OiAiVHVlLCAyIEphbiAyMDI0IDAzOjA0IC0wMDowMCIsIHZhbHVlOiAiMjAyNC0wMS0wMlQwMzowNCIsIHByZWNpc2lvbjogIm1pbnV0ZSIsIHpvbmU6ICJvbWl0dGVkIiwgem9uZVRleHQ6ICItMDA6MDAiLCB3YW50U2VudDogdHJ1ZX0sCisJCXtuYW1lOiAiY29tcGFjdF9uZWdhdGl2ZV96ZXJvIiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCAtMDAwMFxyXG4iLCByYXc6ICJUdWUsIDIgSmFuIDIwMjQgMDM6MDQgLTAwMDAiLCB2YWx1ZTogIjIwMjQtMDEtMDJUMDM6MDQiLCBwcmVjaXNpb246ICJtaW51dGUiLCB6b25lOiAib21pdHRlZCIsIHpvbmVUZXh0OiAiLTAwMDAiLCB3YW50U2VudDogdHJ1ZX0sCisJCXtuYW1lOiAiY29sb25fb2Zmc2V0IiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCArMDU6MzBcclxuIiwgcmF3OiAiVHVlLCAyIEphbiAyMDI0IDAzOjA0ICswNTozMCIsIHZhbHVlOiAiMjAyNC0wMS0wMlQwMzowNCIsIHByZWNpc2lvbjogIm1pbnV0ZSIsIHpvbmU6ICJvZmZzZXQiLCB6b25lVGV4dDogIiswNTozMCIsIHV0YzogIjIwMjQtMDEtMDFUMjE6MzQ6MDAuMDAwMDAwMDAwWiIsIGhhc09mZnNldDogdHJ1ZSwgb2Zmc2V0OiAxOTgwMCwgd2FudFNlbnQ6IHRydWV9LAorCQl7bmFtZTogImNvbXBhY3Rfb2Zmc2V0IiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCAtMDcwMFxyXG4iLCByYXc6ICJUdWUsIDIgSmFuIDIwMjQgMDM6MDQgLTA3MDAiLCB2YWx1ZTogIjIwMjQtMDEtMDJUMDM6MDQiLCBwcmVjaXNpb246ICJtaW51dGUiLCB6b25lOiAib2Zmc2V0Iiwgem9uZVRleHQ6ICItMDcwMCIsIHV0YzogIjIwMjQtMDEtMDJUMTA6MDQ6MDAuMDAwMDAwMDAwWiIsIGhhc09mZnNldDogdHJ1ZSwgb2Zmc2V0OiAtMjUyMDAsIHdhbnRTZW50OiB0cnVlfSwKKwkJe25hbWU6ICJjb2xvbl9saW1pdCIsIGhlYWRlcnM6ICJEYXRlOiBUdWUsIDIgSmFuIDIwMjQgMDM6MDQgKzE4OjAwXHJcbiIsIHJhdzogIlR1ZSwgMiBKYW4gMjAyNCAwMzowNCArMTg6MDAiLCB2YWx1ZTogIjIwMjQtMDEtMDJUMDM6MDQiLCBwcmVjaXNpb246ICJtaW51dGUiLCB6b25lOiAib2Zmc2V0Iiwgem9uZVRleHQ6ICIrMTg6MDAiLCB1dGM6ICIyMDI0LTAxLTAxVDA5OjA0OjAwLjAwMDAwMDAwMFoiLCBoYXNPZmZzZXQ6IHRydWUsIG9mZnNldDogNjQ4MDAsIHdhbnRTZW50OiB0cnVlfSwKKwkJe25hbWU6ICJjb21wYWN0X2xpbWl0IiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCArMTgwMFxyXG4iLCByYXc6ICJUdWUsIDIgSmFuIDIwMjQgMDM6MDQgKzE4MDAiLCB2YWx1ZTogIjIwMjQtMDEtMDJUMDM6MDQiLCBwcmVjaXNpb246ICJtaW51dGUiLCB6b25lOiAib2Zmc2V0Iiwgem9uZVRleHQ6ICIrMTgwMCIsIHV0YzogIjIwMjQtMDEtMDFUMDk6MDQ6MDAuMDAwMDAwMDAwWiIsIGhhc09mZnNldDogdHJ1ZSwgb2Zmc2V0OiA2NDgwMCwgd2FudFNlbnQ6IHRydWV9LAorCQl7bmFtZTogInNob3J0X251bWVyaWNfb25lX2RpZ2l0IiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCArNFxyXG4iLCByYXc6ICJUdWUsIDIgSmFuIDIwMjQgMDM6MDQgKzQiLCB2YWx1ZTogIjIwMjQtMDEtMDJUMDM6MDQiLCBwcmVjaXNpb246ICJtaW51dGUiLCB6b25lOiAiaW52YWxpZCIsIHpvbmVUZXh0OiAiKzQiLCB3YW50U2VudDogdHJ1ZX0sCisJCXtuYW1lOiAic2hvcnRfbnVtZXJpY190d29fZGlnaXRzIiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCArMDRcclxuIiwgcmF3OiAiVHVlLCAyIEphbiAyMDI0IDAzOjA0ICswNCIsIHZhbHVlOiAiMjAyNC0wMS0wMlQwMzowNCIsIHByZWNpc2lvbjogIm1pbnV0ZSIsIHpvbmU6ICJpbnZhbGlkIiwgem9uZVRleHQ6ICIrMDQiLCB3YW50U2VudDogdHJ1ZX0sCisJCXtuYW1lOiAiY29sb25fb3V0X29mX3JhbmdlIiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCArMjU6MDBcclxuIiwgcmF3OiAiVHVlLCAyIEphbiAyMDI0IDAzOjA0ICsyNTowMCIsIHZhbHVlOiAiMjAyNC0wMS0wMlQwMzowNCIsIHByZWNpc2lvbjogIm1pbnV0ZSIsIHpvbmU6ICJpbnZhbGlkIiwgem9uZVRleHQ6ICIrMjU6MDAiLCB3YW50U2VudDogdHJ1ZX0sCisJCXtuYW1lOiAiY29tcGFjdF9vdXRfb2ZfcmFuZ2UiLCBoZWFkZXJzOiAiRGF0ZTogVHVlLCAyIEphbiAyMDI0IDAzOjA0ICsxOTMwXHJcbiIsIHJhdzogIlR1ZSwgMiBKYW4gMjAyNCAwMzowNCArMTkzMCIsIHZhbHVlOiAiMjAyNC0wMS0wMlQwMzowNCIsIHByZWNpc2lvbjogIm1pbnV0ZSIsIHpvbmU6ICJpbnZhbGlkIiwgem9uZVRleHQ6ICIrMTkzMCIsIHdhbnRTZW50OiB0cnVlfSwKKwkJe25hbWU6ICJyZmNfZml4ZWRfem9uZSIsIGhlYWRlcnM6ICJEYXRlOiBUdWUsIDIgSmFuIDIwMjQgMDM6MDQgRVNUXHJcbiIsIHJhdzogIlR1ZSwgMiBKYW4gMjAyNCAwMzowNCBFU1QiLCB2YWx1ZTogIjIwMjQtMDEtMDJUMDM6MDQiLCBwcmVjaXNpb246ICJtaW51dGUiLCB6b25lOiAicmZjNTMyMiIsIHpvbmVUZXh0OiAiRVNUIiwgdXRjOiAiMjAyNC0wMS0wMlQwODowNDowMC4wMDAwMDAwMDBaIiwgaGFzT2Zmc2V0OiB0cnVlLCBvZmZzZXQ6IC0xODAwMCwgd2FudFNlbnQ6IHRydWV9LAorCQl7bmFtZTogInVua25vd25fem9uZSIsIGhlYWRlcnM6ICJEYXRlOiBUdWUsIDIgSmFuIDIwMjQgMDM6MDQgQ0VUXHJcbiIsIHJhdzogIlR1ZSwgMiBKYW4gMjAyNCAwMzowNCBDRVQiLCB2YWx1ZTogIjIwMjQtMDEtMDJUMDM6MDQiLCBwcmVjaXNpb246ICJtaW51dGUiLCB6b25lOiAibmFtZWQiLCB6b25lVGV4dDogIkNFVCIsIHdhbnRTZW50OiB0cnVlfSwKKwkJe25hbWU6ICJtaWxpdGFyeV96b25lIiwgaGVhZGVyczogIkRhdGU6IFR1ZSwgMiBKYW4gMjAyNCAwMzowNCBaXHJcbiIsIHJhdzogIlR1ZSwgMiBKYW4gMjAyNCAwMzowNCBaIiwgdmFsdWU6ICIyMDI0LTAxLTAyVDAzOjA0IiwgcHJlY2lzaW9uOiAibWludXRlIiwgem9uZTogIm9taXR0ZWQiLCB6b25lVGV4dDogIloiLCB3YW50U2VudDogdHJ1ZX0sCisJCXtuYW1lOiAibm9fem9uZSIsIGhlYWRlcnM6ICJEYXRlOiBUdWUsIDIgSmFuIDIwMjQgMDM6MDRcclxuIiwgcmF3OiAiVHVlLCAyIEphbiAyMDI0IDAzOjA0IiwgdmFsdWU6ICIyMDI0LTAxLTAyVDAzOjA0IiwgcHJlY2lzaW9uOiAibWludXRlIiwgem9uZTogIm9taXR0ZWQiLCB3YW50U2VudDogdHJ1ZX0sCisJCXtuYW1lOiAiZnJhY3Rpb25fdHdvX2RpZ2l0X3llYXJfY29tbWVudCIsIGhlYWRlcnM6ICJEYXRlOiBUdWUsIDIgSmFuIDI0IDAzOjA0OjA1LjEyNSArMDAwMCAoc3ludGhldGljKVxyXG4iLCByYXc6ICJUdWUsIDIgSmFuIDI0IDAzOjA0OjA1LjEyNSArMDAwMCAoc3ludGhldGljKSIsIHZhbHVlOiAiMjAyNC0wMS0wMlQwMzowNDowNS4xMjUiLCBwcmVjaXNpb246ICJmcmFjdGlvbiIsIHpvbmU6ICJ1dGMiLCB6b25lVGV4dDogIiswMDAwIiwgdXRjOiAiMjAyNC0wMS0wMlQwMzowNDowNS4xMjUwMDAwMDBaIiwgd2FudFNlbnQ6IHRydWV9LAorCX0geworCQl0LlJ1bih0Yy5uYW1lLCBmdW5jKHQgKnRlc3RpbmcuVCkgeworCQkJcGF5bG9hZCA6PSBbXWJ5dGUoIkZyb206IEFkYSA8YWRhQGV4YW1wbGUudGVzdD5cclxuIiArIHRjLmhlYWRlcnMgKyAiXHJcbmJvZHkiKQorCQkJbWV0YWRhdGEsIGVyciA6PSBFeHRyYWN0U291cmNlTWV0YWRhdGEodC5Db250ZXh0KCksIHNvdXJjZU1ldGFkYXRhVGVzdFNwb29sKHQpLCBwYXlsb2FkKQorCQkJcmVxdWlyZS5Ob0Vycm9yKHQsIGVycikKKwkJCWlucHV0IDo9IGNhbm9uaWNhbERvY3VtZW50RXZlbnRJbnB1dCh0LCBtZXRhZGF0YS5GaWVsZHMpCisJCQlyZWNvcmQsIF8sIGVyciA6PSBEZXJpdmVEb2N1bWVudEV2ZW50cyhpbnB1dCkKKwkJCXJlcXVpcmUuTm9FcnJvcih0LCBlcnIpCisJCQl2YXIgc2VudCBbXWRvY3VtZW50LkRvY3VtZW50RXZlbnRWMQorCQkJZm9yIF8sIGV2ZW50IDo9IHJhbmdlIHJlY29yZC5FdmVudHMgeworCQkJCWlmIGV2ZW50LkRhdGVLaW5kID09ICJzZW50IiB7CisJCQkJCXNlbnQgPSBhcHBlbmQoc2VudCwgZXZlbnQpCisJCQkJfQorCQkJfQorCQkJaWYgIXRjLndhbnRTZW50IHsKKwkJCQlyZXF1aXJlLkVtcHR5KHQsIHNlbnQsICJyZWplY3RlZCBvciBhbWJpZ3VvdXMgRGF0ZSBtdXN0IG5vdCB5aWVsZCBhIHNlbnQgZXZlbnQiKQorCQkJCXJldHVybgorCQkJfQorCQkJcmVxdWlyZS5MZW4odCwgc2VudCwgMSwgImV4dHJhY3RlZCBEYXRlIG11c3QgeWllbGQgb25lIHNlbnQgZXZlbnQiKQorCQkJZXZlbnQgOj0gc2VudFswXQorCQkJcmVxdWlyZS5FcXVhbCh0LCB0Yy5yYXcsIGV2ZW50LlJhd1ZhbHVlKQorCQkJcmVxdWlyZS5FcXVhbCh0LCB0Yy52YWx1ZSwgZXZlbnQuRGF0ZVZhbHVlKQorCQkJcmVxdWlyZS5FcXVhbCh0LCB0Yy5wcmVjaXNpb24sIGV2ZW50LlByZWNpc2lvbikKKwkJCWlmIHRjLnByZWNpc2lvbiA9PSAiZnJhY3Rpb24iIHsKKwkJCQlyZXF1aXJlLkVxdWFsKHQsIDMsIGV2ZW50LkZyYWN0aW9uRGlnaXRzKQorCQkJfQorCQkJcmVxdWlyZS5FcXVhbCh0LCB0Yy56b25lLCBldmVudC5UaW1lem9uZUtpbmQpCisJCQlyZXF1aXJlLkVxdWFsKHQsIHRjLnpvbmVUZXh0LCBldmVudC5ab25lVGV4dCkKKwkJCXJlcXVpcmUuRXF1YWwodCwgdGMudXRjLCBldmVudC5VVENLZXkpCisJCQlpZiB0Yy5oYXNPZmZzZXQgeworCQkJCXJlcXVpcmUuTm90TmlsKHQsIGV2ZW50Lk9mZnNldFNlY29uZHMpCisJCQkJcmVxdWlyZS5FcXVhbCh0LCB0Yy5vZmZzZXQsICpldmVudC5PZmZzZXRTZWNvbmRzKQorCQkJfSBlbHNlIHsKKwkJCQlyZXF1aXJlLk5pbCh0LCBldmVudC5PZmZzZXRTZWNvbmRzKQorCQkJfQorCQl9KQorCX0KK30K";
  const testPath = "internal/processing/document_events_email_zone_test.go";
  const runnerPath = "frontend/screenshots/report-export.run.mjs";
  const packageName = "go.kenn.io/docbank/internal/processing";
  const testName = "TestEmailDateExtractionToEvent";
  const hypotheses = [
    "weekday_mismatch",
    "unknown_weekday",
    "colon_positive_zero",
    "colon_negative_zero",
    "colon_offset",
    "colon_limit",
    "compact_limit",
    "short_numeric_one_digit",
    "short_numeric_two_digits",
    "colon_out_of_range",
    "compact_out_of_range",
  ];
  const controls = [
    "invalid_calendar",
    "ambiguous_duplicate",
    "valid_weekday_compact_utc",
    "compact_negative_zero",
    "compact_offset",
    "rfc_fixed_zone",
    "unknown_zone",
    "military_zone",
    "no_zone",
    "fraction_two_digit_year_comment",
  ];
  const output = path.join(
    repository,
    "frontend/test-results/playwright/report-export-email-failing.json",
  );
  const hash = (bytes) => createHash("sha256").update(bytes).digest("hex");
  const report = {
    source,
    source_tree: sourceTree,
    input_tree: inputTree,
    patch_sha256: patchSHA256,
    patch_base64: patchBase64,
    patch_paths: [testPath],
    baseline_sha256: "3de7c9c92adeb89c5352bf8881811946988d605927d00bbf37871b7567b3e0a7",
    patched_sha256: "28847dcdf91b9db1d0d6d7edcea103436cb0517f7db3f0605d0f92ad8d141950",
    proof: null,
    proof_tree: null,
    proof_parents: [],
    checkout: commit,
    checkout_tree: null,
    checkout_parents: [],
    checkout_status_before: null,
    checkout_status_after: null,
    hypotheses,
    controls,
    commands: [],
    modes: [],
    harness_valid: false,
    error: null,
    cleanup: { worktrees: [], scratch_removed: false, errors: [] },
  };
  const command = (label, executable, args, cwd = repository, envExtra = {}, input) => {
    const result = spawnSync(executable, args, {
      cwd,
      env: { ...process.env, ...envExtra },
      input,
      encoding: "utf8",
      timeout: 600_000,
      maxBuffer: 64 * 1024 * 1024,
    });
    const entry = {
      label,
      executable,
      args,
      cwd,
      env_overrides: envExtra,
      status: result.status,
      signal: result.signal,
      error: result.error?.message ?? null,
      stdout: result.stdout ?? "",
      stderr: result.stderr ?? "",
    };
    report.commands.push(entry);
    return entry;
  };
  const requireCommand = (entry) => {
    if (entry.error) throw new Error(`${entry.label}: ${entry.error}`);
    assert.equal(entry.status, 0, `${entry.label} failed`);
    assert.equal(entry.signal, null, `${entry.label} terminated by signal`);
    return entry.stdout;
  };
  const g = (label, cwd, ...args) => requireCommand(command(label, "git", args, cwd)).trim();
  const names = (text) => text.split("\0").filter(Boolean).sort();
  const parents = (id) =>
    g(`raw commit ${id}`, repository, "cat-file", "-p", id)
      .split("\n\n", 1)[0]
      .split("\n")
      .filter((line) => line.startsWith("parent "))
      .map((line) => line.slice(7));
  let scratchRoot;
  try {
    const event = JSON.parse(await readFile(process.env.GITHUB_EVENT_PATH, "utf8"));
    report.proof = event.pull_request.head.sha;
    assert.equal(event.pull_request.base.sha, source);
    assert.match(report.proof, /^[0-9a-f]{40}$/);
    requireCommand(
      command("fetch source and proof", "git", [
        "fetch",
        "--no-tags",
        "--depth=1",
        "origin",
        source,
        report.proof,
      ]),
    );
    assert.equal(g("source tree", repository, "rev-parse", `${source}^{tree}`), sourceTree);
    report.proof_parents = parents(report.proof);
    assert.deepEqual(report.proof_parents, [source]);
    report.checkout_parents = parents(commit);
    if (commit !== report.proof) assert.deepEqual(report.checkout_parents, [source, report.proof]);
    report.checkout_tree = g("checkout tree", repository, "rev-parse", "HEAD^{tree}");
    report.proof_tree = g("proof tree", repository, "rev-parse", `${report.proof}^{tree}`);
    assert.equal(report.checkout_tree, report.proof_tree);
    assert.deepEqual(
      names(
        requireCommand(
          command("proof paths", "git", ["diff", "--name-only", "-z", source, report.proof, "--"]),
        ),
      ),
      [runnerPath],
    );
    report.checkout_status_before = g(
      "checkout status before",
      repository,
      "status",
      "--porcelain=v1",
      "--untracked-files=all",
    );
    assert.equal(report.checkout_status_before, "");
    const patch = Buffer.from(patchBase64, "base64");
    assert.equal(hash(patch), patchSHA256);
    scratchRoot = await mkdtemp(path.join(tmpdir(), "docbank-email-failing-"));
    for (const [mode, cgo] of [
      ["cgo", "1"],
      ["purego", "0"],
    ]) {
      const worktree = path.join(scratchRoot, mode);
      const item = {
        mode,
        cgo,
        worktree,
        added: false,
        input_tree: null,
        test_command: null,
        events: [],
        parse_errors: [],
        cases: {},
        harness_valid: false,
        outcome: "not_run",
        error: null,
      };
      report.modes.push(item);
      try {
        requireCommand(
          command(`add ${mode} worktree`, "git", ["worktree", "add", "--detach", worktree, source]),
        );
        item.added = true;
        assert.equal(g(`${mode} head`, worktree, "rev-parse", "HEAD"), source);
        assert.equal(
          g(`${mode} clean`, worktree, "status", "--porcelain=v1", "--untracked-files=all"),
          "",
        );
        assert.equal(hash(await readFile(path.join(worktree, testPath))), report.baseline_sha256);
        requireCommand(
          command(
            `${mode} apply test patch`,
            "git",
            ["apply", "--index", "--binary", "-"],
            worktree,
            {},
            patch,
          ),
        );
        assert.deepEqual(
          names(
            requireCommand(
              command(
                `${mode} source paths`,
                "git",
                ["diff", "--cached", "--name-only", "-z", "HEAD", "--"],
                worktree,
              ),
            ),
          ),
          [testPath],
        );
        item.input_tree = g(`${mode} input tree`, worktree, "write-tree");
        assert.equal(item.input_tree, inputTree);
        assert.equal(hash(await readFile(path.join(worktree, testPath))), report.patched_sha256);
        requireCommand(
          command(`${mode} go version`, "go", ["version"], worktree, {
            CGO_ENABLED: cgo,
            GOWORK: "off",
          }),
        );
        const run = command(
          `${mode} focused test`,
          "go",
          [
            "test",
            "-json",
            "-count=1",
            "-tags",
            "fts5",
            "./internal/processing",
            "-run",
            "^TestEmailDateExtractionToEvent$",
          ],
          worktree,
          { CGO_ENABLED: cgo, GOWORK: "off" },
        );
        item.test_command = run;
        for (const line of run.stdout.split("\n")) {
          if (!line) continue;
          try {
            item.events.push(JSON.parse(line));
          } catch {
            item.parse_errors.push(line);
          }
        }
        for (const name of [
          testName,
          ...[...hypotheses, ...controls].map((name) => `${testName}/${name}`),
        ]) {
          const events = item.events.filter(
            (event) => event.Package === packageName && event.Test === name,
          );
          item.cases[name] = {
            started: events.some((event) => event.Action === "run"),
            terminal: events
              .filter((event) => ["pass", "fail", "skip"].includes(event.Action))
              .map((event) => event.Action),
            output: events
              .filter((event) => event.Action === "output")
              .map((event) => event.Output)
              .join(""),
          };
        }
        assert.equal(g(`${mode} after tree`, worktree, "write-tree"), inputTree);
        assert.deepEqual(
          names(
            requireCommand(
              command(
                `${mode} unstaged paths`,
                "git",
                ["diff", "--name-only", "-z", "--"],
                worktree,
              ),
            ),
          ),
          [],
        );
        assert.deepEqual(
          names(
            requireCommand(
              command(
                `${mode} untracked paths`,
                "git",
                ["ls-files", "--others", "--exclude-standard", "-z"],
                worktree,
              ),
            ),
          ),
          [],
        );
        const allCases = Object.values(item.cases);
        const controlsPass = controls.every((name) => {
          const c = item.cases[`${testName}/${name}`];
          return c.started && c.terminal.length === 1 && c.terminal[0] === "pass";
        });
        const failed = hypotheses.filter(
          (name) => item.cases[`${testName}/${name}`].terminal[0] === "fail",
        );
        item.failed_assertion_sites = Object.fromEntries(
          failed.map((name) => {
            const text = item.cases[`${testName}/${name}`].output;
            const sites = [
              ...text.matchAll(/document_events_email_zone_test\.go:(96|99|10[1-9]|11[1-4])\b/g),
            ].map((match) => Number(match[1]));
            return [name, sites];
          }),
        );
        item.harness_valid =
          !run.error &&
          run.signal === null &&
          [0, 1].includes(run.status) &&
          item.parse_errors.length === 0 &&
          allCases.every((c) => c.started && c.terminal.length === 1 && c.terminal[0] !== "skip") &&
          controlsPass &&
          ((run.status === 0 &&
            item.cases[testName].terminal[0] === "pass" &&
            failed.length === 0) ||
            (run.status === 1 && item.cases[testName].terminal[0] === "fail" && failed.length > 0));
        item.failed_hypotheses = failed;
        item.unclassified_failures = failed.filter(
          (name) => item.failed_assertion_sites[name].length === 0,
        );
        if (item.unclassified_failures.length) item.harness_valid = false;
        item.outcome = !item.harness_valid
          ? "invalid_or_unexpected_control_build_fixture"
          : failed.length
            ? "named_hypothesis_failures_require_owner_assertion_review"
            : "no_hypothesis_failure_observed";
        if (!item.harness_valid)
          carrierError ??= new Error(`${mode} test evidence invalid or controls failed`);
        if (item.harness_valid && failed.length === 0)
          carrierError ??= new Error(`${mode} produced no named failing hypothesis`);
      } catch (error) {
        item.error = error.stack ?? String(error);
        item.outcome = "harness_setup_or_inspection_failure";
        carrierError ??= error;
      }
    }
    report.harness_valid =
      report.modes.length === 2 && report.modes.every((item) => item.harness_valid);
    report.named_failure_evidence =
      report.harness_valid && report.modes.every((item) => item.failed_hypotheses.length > 0);
  } catch (error) {
    report.error = error.stack ?? String(error);
    carrierError ??= error;
  } finally {
    for (const item of report.modes) {
      const cleanup = { mode: item.mode, path: item.worktree, removed: false, error: null };
      report.cleanup.worktrees.push(cleanup);
      try {
        // Try removal even when add failed, because Git may have created a partial worktree.
        requireCommand(
          command(`remove ${item.mode} worktree`, "git", [
            "worktree",
            "remove",
            "--force",
            item.worktree,
          ]),
        );
        cleanup.removed = true;
      } catch (error) {
        cleanup.error = error.stack ?? String(error);
        report.cleanup.errors.push(cleanup.error);
      }
    }
    if (scratchRoot && report.cleanup.worktrees.every((item) => item.removed)) {
      try {
        await rm(scratchRoot, { recursive: true, force: true });
        report.cleanup.scratch_removed = true;
      } catch (error) {
        report.cleanup.errors.push(error.stack ?? String(error));
      }
    } else if (!scratchRoot) report.cleanup.scratch_removed = true;
    try {
      report.checkout_status_after = g(
        "checkout status after",
        repository,
        "status",
        "--porcelain=v1",
        "--untracked-files=all",
      );
      assert.equal(report.checkout_status_after, "");
      assert.equal(g("checkout head after", repository, "rev-parse", "HEAD"), commit);
    } catch (error) {
      report.cleanup.errors.push(error.stack ?? String(error));
    }
    if (report.cleanup.errors.length) carrierError ??= new Error("email carrier cleanup failed");
    await mkdir(path.dirname(output), { recursive: true });
    await writeFile(output, `${JSON.stringify(report, null, 2)}\n`);
  }
}
const scratch = await mkdtemp(path.join(tmpdir(), "docbank-browser-build-"));
const failures = [];
// Playwright stops its own run, retries included, before the watchdog kills it,
// so its JSON reporter always writes the result.
const browserWatchdog = 360_000;
const browserGlobalTimeout = browserWatchdog - 30_000;
try {
  for (const [mode, cgo] of [
    ["cgo", "1"],
    ["purego", "0"],
  ]) {
    console.log(`Building CGO_ENABLED=${cgo} (${mode})`);
    const binary = path.join(
      scratch,
      `docbank-${mode}${process.platform === "win32" ? ".exe" : ""}`,
    );
    // CI keeps each mode's JSON report for upload.
    const report = process.env.CI
      ? path.join(repository, "frontend/test-results/playwright", `report-export-${mode}.json`)
      : path.join(scratch, `${mode}.json`);
    const env = {
      ...process.env,
      CGO_ENABLED: cgo,
      DOCBANK_SCREENSHOT_BINARY: binary,
      DOCBANK_REPORT_EXPORT_SCREENSHOT_DIR: path.join(
        repository,
        ".superpowers/report-export-browser",
        mode,
      ),
      PLAYWRIGHT_JSON_OUTPUT_FILE: report,
    };
    try {
      await rm(env.DOCBANK_REPORT_EXPORT_SCREENSHOT_DIR, { recursive: true, force: true });
      const build = spawnSync("go", ["build", "-tags", "fts5", "-o", binary, "./cmd/docbank"], {
        cwd: repository,
        env,
        stdio: "inherit",
        timeout: 600_000,
      });
      if (build.error) throw build.error;
      assert.equal(build.status, 0, `${mode} build failed`);
      const run = spawnSync(
        process.execPath,
        [
          "frontend/node_modules/@playwright/test/cli.js",
          "test",
          "report-export.screenshot.ts",
          "--config",
          "frontend/screenshots/playwright.config.ts",
          "--project",
          "chromium",
          "--reporter",
          "line,json",
          "--global-timeout",
          String(browserGlobalTimeout),
        ],
        { cwd: repository, env, stdio: "inherit", timeout: browserWatchdog },
      );
      if (run.error) throw run.error;
      assert.equal(run.status, 0, `${mode} browser case failed`);
      const result = JSON.parse(await readFile(report, "utf8"));
      assert.equal(result.errors.length, 0, `${mode} runner errors`);
      const { expected, unexpected, flaky, skipped } = result.stats;
      assert.deepEqual(
        { expected, unexpected, flaky, skipped },
        { expected: 1, unexpected: 0, flaky: 0, skipped: 0 },
        `${mode} must execute one passing case`,
      );
      console.log(`${mode}: executed=1 passed=1 skipped=0 failed=0`);
    } catch (error) {
      failures.push(mode);
      console.error(`${mode}: ${error.message}`);
    }
  }
} finally {
  await rm(scratch, { recursive: true, force: true });
}
if (failures.length) {
  throw new Error(`Browser qualification failed: ${failures.join(", ")}`);
}
if (carrierError) throw carrierError;
