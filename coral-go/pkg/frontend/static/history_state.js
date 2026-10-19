/* The History tab's page number, behind a getter and a setter so the code that reads and moves it can live in more than one module (#5084). */

let _historyPage = 1; // page number only; all other filter state lives in filterState

export function getHistoryPage() { return _historyPage; }

export function setHistoryPage(page) { _historyPage = page; }
