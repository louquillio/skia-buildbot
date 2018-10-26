/**
 * @module task-driver-sk
 * @description <h2><code>task-driver-sk</code></h2>
 *
 * <p>
 * This element displays information about a Task Driver.
 * </p>
 *
 */
import { html, render } from 'lit-html/lib/lit-extended'
import { $$ } from 'common-sk/modules/dom'
import { localeTime, strDuration } from 'common-sk/modules/human'
import { jsonOrThrow } from 'common-sk/modules/jsonOrThrow'
import { errorMessage } from 'elements-sk/errorMessage'
import { upgradeProperty } from 'elements-sk/upgradeProperty'
import 'elements-sk/collapse-sk'
import 'elements-sk/styles/buttons'


const tr = (contents) => html`<tr>${contents}</tr>`;

const td = (contents) => html`<td>${contents}</td>`;

const propLine = (k, v) => html`
  ${tr(html`${td(k)}${td(v)}`)}
`;

function stepData(s, d) {
  switch(d.type) {
    case "command":
      return propLine("Command", d.data.command.join(" "));
    case "httpRequest":
      return propLine("HTTP Request", d.data.url);
    case "httpResponse":
      return propLine("HTTP Response", d.data.status);
    case "log":
      return propLine("Log (" + d.data.name + ")", html`
          <a href="${ele._logLink(s, d.data.id)}" target="_blank">${d.data.name}</a>
      `);
  }
  return "";
}

const stepProperties = (ele, s) => html`
  <table class="properties">
    ${s.isInfra ? propLine("Infra", s.isInfra) : ""}
    ${propLine("Started", ele._displayTime(s.started))}
    ${propLine("Finished", ele._displayTime(s.finished))}
    ${s.environment
        ? tr(html`${td("Environment")}${td(html`
            ${s.environment.map((env) => tr(td(env)))}
          `)}`)
        : ""
    }
    ${s.data ? s.data.map((d) => stepData(s, d)) : ""}
    ${tr(html`${td("Log (combined)")}${td(html`
        <a href="${ele._logLink(s)}" target="_blank">all logs</a>
    `)}`)}
  </div>
`;

const stepChildren = (ele, s) => html`
  <div class="vert children_link">
    <a id="button_children_${s.id}" on-click=${(ev) => ele._toggleChildren(s)}>
      ${expando(s.expandChildren)}
    </a>
    ${s.steps.length} Children
  </div>
  <collapse-sk id="children_${s.id}" closed?="${!s.expandChildren}">
    ${s.steps.map((s) => step(ele, s))}
  </collapse-sk>
`;

const stepInner = (ele, s) => html`
    <collapse-sk id="props_${s.id}" closed?="${!s.expandProps}">
      ${stepProperties(ele, s)}
    </collapse-sk>
    ${s.steps && s.steps.length > 0 ? stepChildren(ele, s) : ""}
`;

const expando = (expanded) => html`<span class="expando">[${expanded ? "-" : "+"}]</span>`;

const step = (ele, s) => html`
  <div class$="${ele._stepClass(s)}">
    <div class="vert">
      <div class$="${ele._stepNameClass(s)}">${s.name}</div>
      <div class="horiz duration">${ele._duration(s.started, s.finished)}</div>
      <a class="horiz" id="button_props_${s.id}" on-click=${(ev) => ele._toggleProps(s)}>
        ${expando(s.expandProps)}
      </a>
    </div>
    ${stepInner(ele, s)}
  </div>
`;

const template = (ele) => step(ele, ele.data);

window.customElements.define('task-driver-sk', class extends HTMLElement {
  constructor() {
    super();
    this._data = {};
  }

  connectedCallback() {
    upgradeProperty(this, 'data');
    this._render();
  }

  _parseDate(ts) {
    if (!ts) {
      return null;
    }
    try {
      let d = new Date(ts);
      if (d.getFullYear() < 1970) {
        return null;
      }
      return d;
    } catch(e) {
      return null;
    }
  }

  _displayTime(ts) {
    let d = this._parseDate(ts);
    if (!d) {
      return "-";
    }
    return localeTime(d);
  }

  _duration(started, finished) {
    let startedDate = this._parseDate(started);
    if (!startedDate) {
      // PubSub messages may arrive out of order, so it's possible that we don't
      // have a start timestemp for a step. Don't try to compute a duration in
      // that case.
      return "(no start time)";
    }
    let finishedDate = this._parseDate(finished);
    if (!finishedDate) {
      // If we don't have a finished timestamp for the step, we can assume that
      // the step simply hasn't finished yet. Compute the duration of the step
      // so far.
      finishedDate = new Date();
    }
    // TODO(borenet): strDuration only gets down to seconds. It'd be nice to
    // give millisecond precision.
    let duration = strDuration((finishedDate.getTime() - startedDate.getTime()) / 1000);
    return duration;
  }

  _toggleChildren(step) {
    let collapse = document.getElementById("children_" + step.id);
    collapse.closed = !collapse.closed;
    step.expandChildren = !collapse.closed;
    this._render();
  }

  _toggleProps(step) {
    let collapse = document.getElementById("props_" + step.id);
    collapse.closed = !collapse.closed;
    step.expandProps = !collapse.closed;
    this._render();
  }

  _logLink(step, logId) {
    // Build the logs filter.
    let project = "skia-swarming-bots";
    let taskId = this._data.id;
    let logName = `projects/${project}/logs/task-driver`;
    let filter = {
      "logName": logName,
      "labels.taskId": taskId,
      "textPayload": "*",
    };
    if (step.parent) {
      filter["labels.stepId"] = step.id;
    }
    if (logId) {
      filter["labels.logId"] = logId;
    }

    // Stringify the filter.
    let filterStr = "";
    for (var key in filter) {
      if (filterStr) {
        filterStr += "\n";
      }
      filterStr += key + "=\"" + filter[key] + "\"";
    }

    // Gather the remaining URL params.
    let params = {
      "project": project,
      "logName": logName,
      "minLogLevel": 1,
      "dateRangeUnbound": "backwardInTime",
      "advancedFilter": filterStr,
    };

    // Build the URL.
    let rv = "https://pantheon.corp.google.com/logs/viewer";
    let first = true;
    for (var key in params) {
      if (first) {
        rv += "?";
        first = false;
      } else {
        rv += "&"
      }
      rv += key + "=" + encodeURIComponent(params[key]);
    }
    return rv;
  }

  // Return true if the step is interesting, ie. it has a result other than
  // SUCCESS (including not yet finished).
  _stepIsInteresting(step) {
    return step.result != "SUCCESS";
  }

  // Process the step data. Return true if the current step is interesting.
  _process(step) {
    // Sort the step data, so that the properties end up in a predictable order.
    if (step.data) {
      step.data.sort(function(a, b) {
        if (a.type < b.type) {
          return -1;
        } else if (a.type > b.type) {
          return 1;
        }
        if (a.data.name < b.data.name) {
          return -1;
        }
        return 1;
      });
    }

    // We expand the children of this step if this step is interesting AND if
    // any of the children are interesting. Note that parent steps which do not
    // inherit the failure of one of their children will not be considered
    // interesting unless they fail for another reason.
    let anyChildInteresting = false;
    for (var i = 0; i < (step.steps || []).length; i++) {
      if (this._process(step.steps[i])) {
        anyChildInteresting = true;
      }
    }
    let isInteresting = this._stepIsInteresting(step);
    step.expandChildren = false;
    if (isInteresting && anyChildInteresting) {
      step.expandChildren = true;
    }

    // Step properties take up a lot of space on the screen. Only display them
    // if the step is interesting AND it has no interesting children.
    // Unsuccessful steps which have unsuccessful children are most likely to
    // have inherited the result of their children and so their properties are
    // not as important of those of the failed child step.
    step.expandProps = isInteresting && !anyChildInteresting;

    return isInteresting;
  }

  get data() { return this._data; }
  set data(val) {
    this._process(val);
    this._data = val;
    this._render();
  }

  _render() {
    render(template(this), this);
  }

  _reload() {
    fetch(`/json/task`)
      .then(jsonOrThrow)
      .then((json) => {
        this._data = json;
        this._render();
      }
    ).catch((e) => {
      errorMessage('Failed to load task driver', 10000);
      this.data = {};
      this._render();
    });
  }

  _stepClass(s) {
    let res = s.result;
    if (s.isInfra && s.result == "FAILURE") {
      res = "EXCEPTION";
    }
    if (!res) {
      res = "IN_PROGRESS";
    }
    return "step " + res;
  }

  _stepNameClass(s) {
    if (s.parent) {
      return "horiz h4";
    }
    return "horiz h2";
  }
});