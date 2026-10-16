/* Moved out of modals.js as it was (#3844): no behaviour change. */
import { importOnce } from './import_once.js';
const { _acfServer, _getAgentModels, _getDefaultModels } = await importOnce('./launch_models.js');

// A persisted model is not automatically a user edit. Treat known provider
// defaults/models as replaceable when the provider changes, while preserving
// an explicitly chosen custom model across the switch.
export function _classifyLoadedACFModel(container, agentType, model) {
    if (!container || !model) return;
    const input = container.querySelector('.acf-model');
    const initialModel = model;
    const acfServer = _acfServer(container);
    Promise.all([_getDefaultModels(acfServer), _getAgentModels(acfServer)]).then(([defaults, models]) => {
        if (container.dataset.acfModelDirty === 'true' || input?.value !== initialModel) return;
        const knownModels = Array.isArray(models?.[agentType]) ? models[agentType] : [];
        const isKnown = defaults?.[agentType] === initialModel || knownModels.includes(initialModel);
        container.dataset.acfModelDirty = isKnown ? 'false' : 'true';
        container.dataset.acfModelClassifying = 'false';
    }).catch(() => {
        // If the catalog cannot be read, preserve the explicit value rather
        // than risking data loss.
        container.dataset.acfModelClassifying = 'false';
        container.dataset.acfModelDirty = 'true';
    });
}
