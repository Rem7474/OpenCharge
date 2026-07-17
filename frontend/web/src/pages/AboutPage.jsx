export default function AboutPage() {
  return (
    <div className="about-page">
      <h1>À propos d'OpenCharge</h1>
      <p>
        OpenCharge visualise les bornes de recharge pour véhicules électriques en France (puis en
        Europe) et compare leurs tarifs entre plusieurs fournisseurs.
      </p>

      <h2>Référentiel des bornes : IRVE</h2>
      <p>
        La localisation et les caractéristiques techniques de chaque point de charge (puissance,
        type de connecteur, opérateur, aménageur) proviennent du schéma national IRVE, consolidé
        par Etalab et republié sur transport.data.gouv.fr. Ces données sont chargées telles
        quelles : OpenCharge ne les reconsolide pas, il les enrichit avec des informations
        tarifaires.
      </p>

      <h2>Sources tarifaires</h2>
      <p>
        Les prix affichés proviennent des sites publics des opérateurs de recharge (Izivia,
        Electra pour l'instant, d'autres sources pourront être ajoutées). Ils sont collectés
        périodiquement et associés à une borne IRVE par proximité géographique — la station
        tarifaire la plus proche dans un rayon de quelques dizaines de mètres est retenue comme
        correspondance.
      </p>

      <h2>Fiabilité des prix</h2>
      <p>
        Les tarifs affichés sont indicatifs : ils reflètent les informations publiées par chaque
        opérateur au moment de la collecte et peuvent ne plus être à jour, notamment en cas de
        changement de grille tarifaire, d'abonnement personnel, ou de frais additionnels (badge,
        stationnement, frais d'occupation) non capturés par la source. Vérifiez toujours le prix
        annoncé sur la borne ou dans l'application de l'opérateur avant de lancer une recharge.
      </p>
      <p>
        Le mode « prix pour une recharge » calcule uniquement le coût de l'énergie (prix au kWh ×
        nombre de kWh choisi) ; il ne prend pas en compte d'éventuels frais de session ou de
        occupation prolongée.
      </p>
    </div>
  );
}
