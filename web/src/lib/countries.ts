import { locale } from '../i18n'

// ISO 3166-1 numeric (as used by the world-atlas map) to alpha-2 (as
// stored by GeoIP). Generated from i18n-iso-countries.
export const numericToAlpha2: Record<string, string> = {"100":"BG","104":"MM","108":"BI","112":"BY","116":"KH","120":"CM","124":"CA","140":"CF","144":"LK","148":"TD","152":"CL","156":"CN","158":"TW","170":"CO","178":"CG","180":"CD","188":"CR","191":"HR","192":"CU","196":"CY","203":"CZ","204":"BJ","208":"DK","214":"DO","218":"EC","222":"SV","226":"GQ","231":"ET","232":"ER","233":"EE","238":"FK","242":"FJ","246":"FI","250":"FR","260":"TF","262":"DJ","266":"GA","268":"GE","270":"GM","275":"PS","276":"DE","288":"GH","300":"GR","304":"GL","320":"GT","324":"GN","328":"GY","332":"HT","340":"HN","348":"HU","352":"IS","356":"IN","360":"ID","364":"IR","368":"IQ","372":"IE","376":"IL","380":"IT","384":"CI","388":"JM","392":"JP","398":"KZ","400":"JO","404":"KE","408":"KP","410":"KR","414":"KW","417":"KG","418":"LA","422":"LB","426":"LS","428":"LV","430":"LR","434":"LY","440":"LT","442":"LU","450":"MG","454":"MW","458":"MY","466":"ML","478":"MR","484":"MX","496":"MN","498":"MD","499":"ME","504":"MA","508":"MZ","512":"OM","516":"NA","524":"NP","528":"NL","540":"NC","548":"VU","554":"NZ","558":"NI","562":"NE","566":"NG","578":"NO","586":"PK","591":"PA","598":"PG","600":"PY","604":"PE","608":"PH","616":"PL","620":"PT","624":"GW","626":"TL","630":"PR","634":"QA","642":"RO","643":"RU","646":"RW","682":"SA","686":"SN","688":"RS","694":"SL","703":"SK","704":"VN","705":"SI","706":"SO","710":"ZA","716":"ZW","724":"ES","728":"SS","729":"SD","732":"EH","740":"SR","748":"SZ","752":"SE","756":"CH","760":"SY","762":"TJ","764":"TH","768":"TG","780":"TT","784":"AE","788":"TN","792":"TR","795":"TM","800":"UG","804":"UA","807":"MK","818":"EG","826":"GB","834":"TZ","840":"US","854":"BF","858":"UY","860":"UZ","862":"VE","887":"YE","894":"ZM","032":"AR","044":"BS","076":"BR","068":"BO","084":"BZ","072":"BW","024":"AO","012":"DZ","050":"BD","064":"BT","004":"AF","051":"AM","040":"AT","008":"AL","056":"BE","090":"SB","036":"AU","031":"AZ","096":"BN","010":"AQ","070":"BA"}

let names: Intl.DisplayNames | null = null
let namesLocale = ''

/** Country name in the interface language, e.g. "Nederland" for NL. */
export function countryName(cc: string) {
  if (!cc) return ''
  try {
    if (!names || namesLocale !== locale()) {
      names = new Intl.DisplayNames([locale()], { type: 'region' })
      namesLocale = locale()
    }
    return names.of(cc.toUpperCase()) ?? cc
  } catch {
    return cc
  }
}

/** Flag emoji for an alpha-2 code. */
export function flag(cc: string) {
  if (!/^[A-Za-z]{2}$/.test(cc)) return ''
  return String.fromCodePoint(...[...cc.toUpperCase()].map((c) => 0x1f1a5 + c.charCodeAt(0)))
}

/** "AS13335 Cloudflare" from the "13335|Cloudflare" keys of top lists. */
export function asLabel(key: string) {
  const [n, org] = key.split('|')
  return [n && 'AS' + n, org].filter(Boolean).join(' ')
}
