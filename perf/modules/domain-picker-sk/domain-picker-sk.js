/**
 * @module modules/domain-picker-sk
 * @description <h2><code>domain-picker-sk</code></h2>
 *
 * Allows picking either a date range for commits, or for
 * picking a number of commits to show before a selected
 * date.
 *
 * @attr {string} force_request_type - A value of 'dense' or 'range' will
 *   force the corresponding request_type to be always set.
 *
 * @evt domain-changed - The event detail.state will contain the updated 'state'.
 *
 */
import { define } from 'elements-sk/define'
import { html, render } from 'lit-html'
import { ElementSk } from '../../../infra-sk/modules/ElementSk'
import dialogPolyfill from 'dialog-polyfill'
import 'elements-sk/radio-sk';
import 'elix/src/DateComboBox.js'
import 'elements-sk/styles/buttons'

const RANGE = 0;
const DENSE = 1;

const _description = (ele) => {
  const begin = new Date(ele._stateBackup.begin*1000);
  const end = new Date(ele._stateBackup.end*1000);
  if (ele._stateBackup.request_type === RANGE) {
    return `${begin.toLocaleDateString()} - ${end.toLocaleDateString()}`;
  } else {
    return `${ele._stateBackup.num_commits} commits ending at ${end.toLocaleDateString()}`;
  }
}

const _toDate = (seconds) => {
  return new Date(seconds*1000);
};

const _request_type = (ele) => {
 if (ele._state.request_type === RANGE) {
   return html`
     <p>Display all points in the date range.</p>
     <label>
       <span>Begin:</span>
       <elix-date-combo-box @date-changed=${ele._beginChange} date=${_toDate(ele._state.begin)}></elix-date-combo-box>
     </label>
     `;
 } else {
   return html`
     <p>Display only the points that have data before the date.</p>
     <label>
       <span>Number of points</span>
       <input @change=${ele._numChanged} type=number .value='${ele._state.num_commits}' min=1 max=5000 list=defaultNumbers>
     </label>
     <datalist id=defaultNumbers>
       <option value=50>
       <option value=100>
       <option value=250>
       <option value=500>
     </datalist>
   `;
 }
};

const _showRadio = (ele) => {
  if (!ele.force_request_type) {
    return html`
      <radiogroup>
        <radio-sk @change=${ele._typeRange} ?checked=${ele._state.request_type === RANGE} label="Date Range"></radio-sk>
        <radio-sk @change=${ele._typeDense} ?checked=${ele._state.request_type === DENSE} label="Dense"     ></radio-sk>
      </radiogroup>
      `;
  } else {
    return html``;
  }
};

const template = (ele) => html`
  <dialog>
    <h2>Graph Domain</h2>
    ${_showRadio(ele)}
    <div class=ranges>
      ${_request_type(ele)}
      <label>
        <span>End:</span>
        <elix-date-combo-box @date-changed=${ele._endChange} date=${_toDate(ele._state.end)}></elix-date-combo-box>
      </label>
    </div>
    <div class=controls>
      <button @click=${ele._cancel}>Cancel</button>
      <button @click=${ele._ok} ?disabled=${ele._isInvalid(ele)}>OK</button>
    </div>
  </dialog>
  <button class=description @click=${ele._edit}>${_description(ele)}</button>
`;

define('domain-picker-sk', class extends ElementSk {
  constructor() {
    super(template);
    const now = Date.now();
    // See the 'state' property setters below for the shape of this._state.
    this._state = {
      begin: Math.floor(now/1000 - 24*60*60),
      end: Math.floor(now/1000),
      num_commits: 50,
      request_type: RANGE,
    };
    this._stateBackup = Object.assign({}, this._state);
    this._description = '';
  }

  connectedCallback() {
    super.connectedCallback();
    this._render();
    this._dialog = this.querySelector('dialog');
    dialogPolyfill.registerDialog(this._dialog);
  }

  _typeRange() {
    this._state.request_type = RANGE;
    this._render();
  }

  _typeDense() {
    this._state.request_type = DENSE;
    this._render();
  }

  _ok() {
    this._stateBackup = Object.assign({}, this._state);
    this._dialog.close();
    const detail = {
      state: this._state,
    }
    this.dispatchEvent(new CustomEvent('domain-changed', {detail: detail, bubbles: true}));
    this._render();
  }

  _beginChange(e) {
    this._state.begin = Math.floor(e.detail.date/1000);
    this._render();
  }

  _endChange(e) {
    this._state.end = Math.floor(e.detail.date/1000);
    this._render();
  }

  _numChanged(e) {
    this._state.num_commits = +e.srcElement.value;
    this._render();
  }

  _edit() {
    this._dialog.showModal();
  }

  _cancel() {
    this._state = Object.assign({}, this._stateBackup);
    this._render();
    this._dialog.close();
  }

  _isInvalid() {
    if (this._state.request_type === RANGE && (this._state.end < this._state.begin)) {
      return true;
    }
    return false;
  }

  static get observedAttributes() {
    return ['force_request_type'];
  }

  /** @prop state {Object} An object that contains the following state:
   *
   *  {
   *    begin:         // unix timestamp in seconds.
   *    end:           // unix timestamp in seconds.
   *    num_commits:   // Number of commits.
   *    request_type:  // 0 for date range, 1 for dense. See dataframe.RequestType.
   *  }
   */
  get state() { return this._stateBackup }
  set state(val) {
    if (!val) {
      return;
    }
    this._state = val;
    this._stateBackup = Object.assign({}, this._state);
    this._render();
  }

  /** @prop force_request_type {string} A value of 'dense' or 'range' will force the corresponding request_type to be always set.
  */
  get force_request_type() { return this.getAttribute('force_request_type'); }
  set force_request_type(val) { this.setAttribute('force_request_type', val); }

  attributeChangedCallback(name, oldValue, newValue) {
    this._render();
  }

  _render() {
    if (this.force_request_type === 'dense') {
      this._state.request_type = DENSE;
      this._stateBackup.request_type = DENSE;
    } else if (this.force_request_type === 'range') {
      this._state.request_type = RANGE;
      this._stateBackup.request_type = RANGE;
    }
    super._render();
  }

});
